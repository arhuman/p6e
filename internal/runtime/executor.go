package runtime

import (
	"context"
	"fmt"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/pipeline"
)

// DefaultMaxConcurrency bounds how many steps run at once when Options does not
// say. It is high enough that a realistic pipeline never notices and low enough
// that a wide fan-out cannot exhaust the process: a 10,000-way fan-out across
// several concurrent executions would otherwise create goroutines without limit.
const DefaultMaxConcurrency = 256

// DefaultAbandonAfter bounds how long Run waits for steps that are still
// running once the execution is winding down, whether because a step failed or
// because the caller's context ended.
//
// It exists because Go cannot stop a goroutine. A node that ignores its context
// would otherwise block Run forever, and no deadline the caller supplied could
// rescue it.
const DefaultAbandonAfter = 5 * time.Second

// Options configures one run. Everything is optional.
type Options struct {
	// WorkflowID identifies the pipeline. Defaults to the plan's name.
	WorkflowID string
	// Inputs supplies the values the pipeline declared, by name. Every declared
	// input must be present and carry the type it declared; a missing or
	// ill-typed one fails that input's step, which stops the run before any
	// node executes.
	//
	// This is what makes one plan reusable across runs: the plan is a function
	// of these, not a constant.
	Inputs map[string]node.Value
	// ExecutionID identifies this run. Defaults to a generated value.
	ExecutionID string
	// MaxConcurrency caps how many steps execute at once. Zero selects
	// DefaultMaxConcurrency. One makes execution sequential.
	//
	// It bounds this run alone. A process running many pipelines at once wants
	// Slots as well: forty plans each entitled to 256 steps is not a bound.
	MaxConcurrency int
	// Slots is a semaphore shared by every run that should compete for one
	// budget, as a daemon's runs do. A step holds one slot for as long as it
	// runs, so this bounds concurrent steps across the whole process rather
	// than concurrent pipelines, which is the quantity that actually costs
	// goroutines. Build one with NewSlots.
	//
	// Nil means unshared, which is the single-run case: MaxConcurrency is then
	// the only bound and no slot is ever taken.
	//
	// A step abandoned under AbandonAfter keeps its slot until it really
	// finishes. That is deliberate: the work is still running and still costing
	// the process, and pretending otherwise would let a wedged node quietly
	// raise the ceiling for everyone else.
	Slots chan struct{}
	// AbandonAfter caps how long Run waits for steps still running after the
	// execution has failed or been cancelled. Zero selects
	// DefaultAbandonAfter.
	AbandonAfter time.Duration
	// DetectMutation checks, at the cost of rendering every step's output
	// twice, whether any node mutated a value it did not own. Violations land
	// in Execution.Mutations. This is a debugging facility, far too expensive
	// to leave on in production.
	DetectMutation bool
	// InlineSoloSteps runs a step on the calling goroutine when it is the only
	// one ready and nothing else is in flight, which removes the goroutine
	// handoff that ADR 0003 measured as most of a step's cost. It roughly halves
	// per-step overhead on a sequential chain.
	//
	// It is off by default because it trades away the timing guarantees above:
	// while an inlined node runs, the coordinator is inside it and cannot
	// abandon it. A node that ignores its context wedges Run rather than leaking
	// a goroutine. Turn this on when the nodes in the pipeline are known to
	// honour cancellation, and leave it off when running anything you do not
	// control (ADR 0008).
	InlineSoloSteps bool
}

// NewSlots builds the shared step budget described by Options.Slots. Size must
// be positive; a zero-capacity pool would let nothing run at all.
func NewSlots(size int) chan struct{} {
	if size <= 0 {
		panic("runtime: slot pool size must be positive")
	}
	return make(chan struct{}, size)
}

var executionCounter atomic.Uint64

// Run executes a compiled plan once and reports what every step did.
//
// The plan is immutable and may be run many times concurrently; all state for
// a run lives in the returned Execution. Run does no name resolution, no
// configuration decoding, and no graph traversal: the compiler did those.
//
// Run does not return an error. A pipeline that fails is a normal outcome
// described by the Execution, not an exception. Check Execution.Failed.
//
// Timing guarantees, which exist because a node that ignores its context cannot
// be stopped:
//
//   - Once ctx is done, Run returns within AbandonAfter.
//   - Once a step has failed, Run returns within AbandonAfter.
//   - Otherwise Run waits, because a step that is merely slow is
//     indistinguishable from one that is stuck.
//
// Steps still running when Run gives up are reported as cancelled and counted
// in Execution.Abandoned. Their goroutines are left behind: that is the cost of
// not being able to kill them, and it is preferable to wedging the caller.
//
// The work is in scheduler, which is this run's coordinator state and the
// operations over it.
func Run(ctx context.Context, plan *pipeline.ExecutionPlan, opts Options) *Execution {
	ex := &Execution{
		ID:         defaultString(opts.ExecutionID, generateExecutionID),
		Plan:       plan,
		Steps:      make([]StepResult, len(plan.Steps)),
		FailedStep: -1,
	}
	for i := range plan.Steps {
		ex.Steps[i] = StepResult{ID: plan.Steps[i].ID, State: StatePending}
	}
	if len(plan.Steps) == 0 {
		return ex
	}

	s := newScheduler(ctx, ex, plan, opts)
	defer s.close()

	s.supplyInputs(opts.Inputs)
	s.finalize(s.loop(ctx.Done()))
	return ex
}

// supply resolves one declared input against what the run provided.
//
// The type check is the run-time counterpart of the compiler's edge check: the
// compiler proved every consumer expects the declared type, so checking the
// supplied value here is what makes that proof hold for values it never saw.
func supply(supplied map[string]node.Value, in pipeline.PlanInput) node.ResultValue {
	value, ok := supplied[in.Name]
	if !ok {
		return node.ResultValue{Err: node.Errf(node.KindInvalidInput, "input_missing",
			"input %q was not supplied", in.Name)}
	}
	if got := value.Type(); got != in.Type {
		return node.ResultValue{Err: node.Errf(node.KindInvalidInput, "input_type",
			"input %q is declared %s but the value supplied is %s", in.Name, in.Type, got)}
	}
	return node.ResultValue{Value: value}
}

// record stores a completion and reports whether it was a failure.
func (e *Execution) record(c completion) bool {
	result := &e.Steps[c.index]
	result.Meta = c.result.Meta

	if c.result.Err != nil {
		result.Err = c.result.Err
		if c.result.Err.Kind == node.KindCancelled {
			result.State = StateCancelled
		} else {
			result.State = StateFailed
		}
		return true
	}

	result.State = StateSucceeded
	result.Value = c.result.Value
	return false
}

type completion struct {
	index  int
	result node.ResultValue
}

// runStep applies the workflow's retry policy to one step. The node reported
// whether its failure is retryable; this decides what to do about it. The node
// itself knows nothing about retries.
func runStep(ctx context.Context, step *pipeline.CompiledStep, ec *node.ExecutionContext, inputs []node.Value) node.ResultValue {
	backoff := step.Retry.Backoff.Duration()

	for attempt := 1; ; attempt++ {
		ec.Attempt = attempt

		started := time.Now()
		result := invoke(ctx, step.Node, ec, inputs)
		result.Meta.Duration = time.Since(started)
		result.Meta.Attempt = attempt

		if result.Err == nil || !result.Err.Retryable || attempt >= step.Retry.MaxAttempts {
			return result
		}
		if err := wait(ctx, backoff); err != nil {
			return node.ResultValue{Err: node.Normalize(err, "cancelled"), Meta: result.Meta}
		}
		backoff *= 2
	}
}

// invoke calls the node and converts a panic into an internal error. A node
// that panics has broken its contract, but it must not take the engine with it:
// other branches of the DAG are still running and their results are still good.
func invoke(ctx context.Context, n node.RuntimeNode, ec *node.ExecutionContext, inputs []node.Value) (result node.ResultValue) {
	defer func() {
		if r := recover(); r != nil {
			err := node.Errf(node.KindInternal, "panic", "node %q panicked: %v", ec.StepID, r)
			err.Cause = fmt.Errorf("%s", debug.Stack())
			result = node.ResultValue{Err: err}
		}
	}()
	return n.Execute(ctx, ec, inputs)
}

// wait sleeps for the backoff unless the execution is cancelled first.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultString(s string, fallback func() string) string {
	if s != "" {
		return s
	}
	return fallback()
}

func generateExecutionID() string {
	return "exec-" + strconv.FormatUint(executionCounter.Add(1), 10)
}

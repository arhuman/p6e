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
	// ExecutionID identifies this run. Defaults to a generated value.
	ExecutionID string
	// MaxConcurrency caps how many steps execute at once. Zero selects
	// DefaultMaxConcurrency. One makes execution sequential.
	MaxConcurrency int
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

	workflowID := opts.WorkflowID
	if workflowID == "" {
		workflowID = plan.Name
	}
	maxConcurrency := opts.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = DefaultMaxConcurrency
	}
	abandonAfter := opts.AbandonAfter
	if abandonAfter <= 0 {
		abandonAfter = DefaultAbandonAfter
	}

	// Cancelling on the first failure stops work that can no longer matter.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One buffer for every step's inputs, carved up by offsets the compiler
	// computed. Each step's slice is disjoint, so the goroutines never share.
	inputs := make([]node.Value, plan.TotalInputs)
	// One ExecutionContext per step, mutated only by that step's goroutine as
	// its attempts progress.
	contexts := make([]node.ExecutionContext, len(plan.Steps))
	// Unmet dependencies per step, counted down instead of re-walking the graph.
	unmet := make([]int, len(plan.Steps))
	for i := range plan.Steps {
		unmet[i] = len(plan.Steps[i].Deps)
	}

	// The send never blocks: the channel holds every step's completion, so an
	// abandoned goroutine that finishes later cannot leak on the send.
	done := make(chan completion, len(plan.Steps))
	// Steps whose dependencies are met, waiting for a concurrency slot. The
	// cursor avoids reslicing, so this allocates once.
	ready := make([]int, 0, len(plan.Steps))
	launched := 0
	inflight := 0
	stopped := false

	// prepare gathers a step's inputs and execution context. It runs only on
	// this goroutine, which owns ex.Steps, so no synchronization is needed: the
	// values are written before the go statement that reads them.
	prepare := func(i int) []node.Value {
		step := &plan.Steps[i]
		in := inputs[step.InputOffset : step.InputOffset+len(step.Deps)]
		for port, dep := range step.Deps {
			in[port] = ex.Steps[dep].Value
		}
		contexts[i] = node.ExecutionContext{
			WorkflowID:  workflowID,
			ExecutionID: ex.ID,
			StepID:      step.ID,
		}
		ex.Steps[i].State = StateRunning
		return in
	}

	launch := func(i int) {
		in := prepare(i)
		step := &plan.Steps[i]
		inflight++
		go func() {
			done <- completion{index: i, result: runStep(runCtx, step, &contexts[i], in)}
		}()
	}

	// pump starts as much ready work as the concurrency cap allows.
	pump := func() {
		for launched < len(ready) && inflight < maxConcurrency {
			launch(ready[launched])
			launched++
		}
	}

	// abandonTimer is armed only once the execution is winding down, so a
	// healthy long-running pipeline is never cut short.
	var abandonTimer *time.Timer
	var abandon <-chan time.Time
	windDown := func() {
		cancel()
		if abandonTimer == nil {
			abandonTimer = time.NewTimer(abandonAfter)
			abandon = abandonTimer.C
		}
	}
	defer func() {
		if abandonTimer != nil {
			abandonTimer.Stop()
		}
	}()

	// callerDone is cleared after it fires. A closed channel stays ready, and
	// selecting on it again would spin.
	callerDone := ctx.Done()

	guard := newMutationGuard(opts.DetectMutation, len(plan.Steps))

	// handle records a completion and releases whatever it unblocked. Both the
	// asynchronous and the inline path go through it, so they cannot drift.
	handle := func(c completion) {
		if ex.record(c) && !stopped {
			stopped = true
			ex.FailedStep = c.index
			windDown()
		}
		guard.record(c.index, ex.Steps[c.index].Value)
		// Steps still in flight are allowed to finish and be recorded, but
		// nothing new starts once the execution has stopped.
		if stopped {
			return
		}
		for _, dependent := range plan.Steps[c.index].Dependents {
			unmet[dependent]--
			if unmet[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}

	ready = append(ready, plan.Roots...)
	abandoned := false

	for !abandoned {
		// The inline fast path: with nothing else running and exactly one step
		// ready, the goroutine and the channel round trip buy nothing, and they
		// are most of what a step costs. The cost is that the coordinator is
		// inside the node while it runs and cannot abandon it, which is why this
		// is opt-in.
		if opts.InlineSoloSteps && !stopped && inflight == 0 && len(ready)-launched == 1 {
			i := ready[launched]
			launched++
			in := prepare(i)
			handle(completion{index: i, result: runStep(runCtx, &plan.Steps[i], &contexts[i], in)})
			continue
		}

		if !stopped {
			pump()
		}
		if inflight == 0 {
			break
		}

		select {
		case c := <-done:
			inflight--
			handle(c)

		case <-callerDone:
			callerDone = nil
			if !stopped {
				stopped = true
				ex.Cancelled = true
			}
			windDown()

		case <-abandon:
			abandoned = true
		}
	}

	if abandoned {
		for i := range ex.Steps {
			if ex.Steps[i].State == StateRunning {
				ex.Steps[i].State = StateCancelled
				ex.Steps[i].Err = node.Errf(node.KindCancelled, "abandoned",
					"step %q was still running %s after the execution stopped", ex.Steps[i].ID, abandonAfter)
				ex.Abandoned++
			}
		}
		if !ex.Failed() {
			ex.Cancelled = true
		}
	}

	// Whatever never started could not: a dependency failed, or the execution
	// stopped first.
	for i := range ex.Steps {
		if ex.Steps[i].State == StatePending {
			ex.Steps[i].State = StateSkipped
		}
	}

	ex.Mutations = guard.check(ex)
	return ex
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

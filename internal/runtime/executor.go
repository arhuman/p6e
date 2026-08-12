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

// Options configures one run. Everything is optional.
type Options struct {
	// WorkflowID identifies the pipeline. Defaults to the plan's name.
	WorkflowID string
	// ExecutionID identifies this run. Defaults to a generated value.
	ExecutionID string
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

	done := make(chan completion, len(plan.Steps))
	inflight := 0
	stopped := false

	// launch runs one step. It is called only from this goroutine, which owns
	// ex.Steps, so gathering inputs here needs no synchronization: the values
	// are written before the go statement that reads them.
	launch := func(i int) {
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
		inflight++
		go func() {
			done <- completion{index: i, result: runStep(runCtx, step, &contexts[i], in)}
		}()
	}

	for _, root := range plan.Roots {
		launch(root)
	}

	for inflight > 0 {
		c := <-done
		inflight--

		result := &ex.Steps[c.index]
		result.Meta = c.result.Meta

		if c.result.Err != nil {
			result.Err = c.result.Err
			if c.result.Err.Kind == node.KindCancelled {
				result.State = StateCancelled
			} else {
				result.State = StateFailed
			}
			if !stopped {
				stopped = true
				ex.FailedStep = c.index
				cancel()
			}
			continue
		}

		result.State = StateSucceeded
		result.Value = c.result.Value

		// Steps still in flight are allowed to finish and be recorded, but
		// nothing new starts once the execution has failed.
		if stopped {
			continue
		}
		for _, dependent := range plan.Steps[c.index].Dependents {
			unmet[dependent]--
			if unmet[dependent] == 0 {
				launch(dependent)
			}
		}
	}

	// Whatever never started could not: a dependency failed, or the execution
	// stopped first.
	for i := range ex.Steps {
		if ex.Steps[i].State == StatePending {
			ex.Steps[i].State = StateSkipped
		}
	}
	return ex
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

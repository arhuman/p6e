package runtime

import (
	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/pipeline"
)

// State is where a step got to in one execution.
type State int

const (
	// StatePending: not started, and its dependencies have not all finished.
	StatePending State = iota
	// StateRunning: handed to a goroutine.
	StateRunning
	// StateSucceeded: produced a value.
	StateSucceeded
	// StateFailed: returned an error the retry policy did not absorb.
	StateFailed
	// StateCancelled: was running when the execution was cancelled.
	StateCancelled
	// StateSkipped: never ran, because something it depends on did not
	// succeed or the execution stopped first.
	StateSkipped
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateRunning:
		return "running"
	case StateSucceeded:
		return "succeeded"
	case StateFailed:
		return "failed"
	case StateCancelled:
		return "cancelled"
	case StateSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// StepResult is what one step did in one execution.
type StepResult struct {
	ID    string
	State State
	// Value is the step's output, set only when State is StateSucceeded. It is
	// the same reference every dependent receives: nothing here is copied.
	Value node.Value
	Err   *node.NodeError
	Meta  node.ResultMeta
}

// Execution is the outcome of running a plan once. It is created per run and
// never shared: the plan is the reusable part.
type Execution struct {
	// ID identifies this run.
	ID string
	// Plan is what was executed.
	Plan *pipeline.ExecutionPlan
	// Steps is indexed the same way as Plan.Steps.
	Steps []StepResult
	// FailedStep is the index of the step that failed the execution, or -1.
	FailedStep int
}

// Failed reports whether the execution did not complete successfully.
func (e *Execution) Failed() bool { return e.FailedStep >= 0 }

// Err returns the failure that ended the execution, or nil.
func (e *Execution) Err() *node.NodeError {
	if !e.Failed() {
		return nil
	}
	return e.Steps[e.FailedStep].Err
}

// Result returns a step's result by ID.
func (e *Execution) Result(stepID string) (StepResult, bool) {
	i, ok := e.Plan.StepIndex(stepID)
	if !ok {
		return StepResult{}, false
	}
	return e.Steps[i], true
}

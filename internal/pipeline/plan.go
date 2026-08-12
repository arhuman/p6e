package pipeline

import "github.com/arhuman/p6e/internal/node"

// CompiledStep is a step with every question already answered. The executor
// reads it and does no lookups of its own: no name resolution, no config
// decoding, no graph traversal.
type CompiledStep struct {
	// ID is the step's name in the pipeline file, kept for reporting.
	ID string
	// Node is the resolved, configured implementation. It is shared by every
	// execution of this plan and must be safe for concurrent use.
	Node node.RuntimeNode
	// Deps holds the indices of the steps feeding this one, in port order
	// (ADR 0002). The executor gathers inputs by indexing results with these.
	Deps []int
	// Dependents holds the indices of steps waiting on this one.
	Dependents []int
	// InputOffset is where this step's inputs live in the execution's single
	// input buffer. Laying the buffer out here means a run allocates one slice
	// for all inputs instead of one per step.
	InputOffset int
	// Retry is the workflow's policy for this step.
	Retry Retry
}

// ExecutionPlan is a compiled pipeline: immutable, reusable, and safe to run
// many times concurrently. All per-execution state lives in the executor.
type ExecutionPlan struct {
	// Name identifies the workflow in logs and execution contexts.
	Name string
	// Steps are ordered by step ID, so a plan is deterministic for a given file.
	Steps []CompiledStep
	// Roots are the indices of steps with no dependencies: where execution
	// starts.
	Roots []int
	// TotalInputs is the size of the input buffer an execution needs, the sum
	// of every step's arity.
	TotalInputs int
}

// Len reports the number of steps.
func (p *ExecutionPlan) Len() int { return len(p.Steps) }

// StepIndex finds a step by ID, for the CLI's benefit. Execution paths use
// indices and never call this.
func (p *ExecutionPlan) StepIndex(id string) (int, bool) {
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return i, true
		}
	}
	return 0, false
}

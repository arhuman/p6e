package runtime

import (
	"fmt"

	"github.com/arhuman/p6e/internal/node"
)

// MutationViolation reports that a step's output was not the same at the end of
// the execution as when the step produced it. Something mutated a value it did
// not own.
type MutationViolation struct {
	// Step is the step whose output changed, which is the victim rather than
	// the culprit: the mutation was done by one of its consumers.
	Step string
	// Consumers are the steps that received the value, one of which mutated it.
	Consumers []string
	Before    string
	After     string
}

func (v MutationViolation) String() string {
	return fmt.Sprintf("output of step %q was mutated after it was produced, by one of %v\n  before: %s\n  after:  %s",
		v.Step, v.Consumers, v.Before, v.After)
}

// mutationGuard detects violations of the immutability rule that the engine
// otherwise only documents.
//
// Values on edges are pointers, so fan-out hands every consumer the same
// reference and a consumer that writes through it corrupts its siblings' input.
// Retry has the same exposure: a node that partially mutates its input before
// failing retries against its own corrupted data. Neither is preventable in Go
// without copying every payload, which is exactly what the design exists to
// avoid, so the answer is to make the violation detectable instead.
//
// This is a debugging facility. It renders every step's output twice and holds
// both renderings for the length of the execution, which is far too expensive
// for production. It is off unless Options.DetectMutation is set.
type mutationGuard struct {
	enabled   bool
	snapshots []string
	taken     []bool
}

func newMutationGuard(enabled bool, steps int) *mutationGuard {
	g := &mutationGuard{enabled: enabled}
	if enabled {
		g.snapshots = make([]string, steps)
		g.taken = make([]bool, steps)
	}
	return g
}

// record snapshots a step's output as it was produced.
func (g *mutationGuard) record(index int, v node.Value) {
	if !g.enabled || v.IsZero() {
		return
	}
	g.snapshots[index] = render(v)
	g.taken[index] = true
}

// check re-renders every snapshotted output and reports the ones that changed.
func (g *mutationGuard) check(ex *Execution) []MutationViolation {
	if !g.enabled {
		return nil
	}
	var violations []MutationViolation
	for i := range ex.Steps {
		if !g.taken[i] {
			continue
		}
		after := render(ex.Steps[i].Value)
		if after == g.snapshots[i] {
			continue
		}
		violations = append(violations, MutationViolation{
			Step:      ex.Steps[i].ID,
			Consumers: ex.consumerNames(i),
			Before:    g.snapshots[i],
			After:     after,
		})
	}
	return violations
}

// render produces a comparable rendering of a value, including the contents of
// byte slices and the unexported fields that %#v reaches. It is not a hash: the
// full text is kept so a violation report can show what changed.
func render(v node.Value) string {
	return fmt.Sprintf("%#v", v.Interface())
}

// consumerNames lists the steps that received a step's output, which is the set
// a mutation could have come from.
func (e *Execution) consumerNames(index int) []string {
	dependents := e.Plan.Steps[index].Dependents
	names := make([]string, 0, len(dependents))
	for _, d := range dependents {
		names = append(names, e.Steps[d].ID)
	}
	return names
}

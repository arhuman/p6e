package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/arhuman/p6e/internal/node"
)

// threeRoots is three independent steps, so two can be queued for a slot while
// a third is in flight.
const threeRoots = `
version: 1
steps:
  a:
    uses: counted.boom
  b:
    uses: counted.boom
  c:
    uses: counted.boom
`

// The shared-pool arm of the select and a failing completion are the one pair
// of events the rest of the suite never exercises together: slot exhaustion and
// cancellation are each covered alone. This pins the interaction, because a run
// that kept claiming slots after it had stopped would launch work whose result
// can no longer matter, and would do it in the function with the highest
// cognitive complexity in the engine.
//
// Every step fails, so the assertion does not depend on which root the
// scheduler happens to launch first.
func TestNoStepStartsOnceTheRunHasStopped(t *testing.T) {
	var started atomic.Int64
	plan := compile(t, threeRoots, func(reg *node.Registry) {
		reg.MustRegister(node.Static("counted.boom", node.NewSource("counted.boom",
			func(context.Context, *node.ExecutionContext) node.Result[*box] {
				started.Add(1)
				return node.Fail[*box](node.Errf(node.KindPermanent, "boom", "exploded"))
			})))
	})

	// Two slots, one of which the test holds, so the run can start exactly one
	// step and must park on the pool for the other two.
	slots := NewSlots(2)
	slots <- struct{}{}
	defer func() { <-slots }()

	ex := Run(t.Context(), plan, Options{Slots: slots})

	if !ex.Failed() {
		t.Fatal("expected the run to fail")
	}
	if n := started.Load(); n != 1 {
		t.Errorf("%d steps started, want exactly 1: nothing may launch once the run has stopped", n)
	}

	var failed, skipped int
	for _, s := range ex.Steps {
		switch s.State {
		case StateFailed:
			failed++
		case StateSkipped:
			skipped++
		}
	}
	if failed != 1 || skipped != 2 {
		t.Errorf("failed=%d skipped=%d, want 1 and 2", failed, skipped)
	}

	// The run must give back what it took and nothing else. A slot leaked here
	// costs a daemon part of its budget for the life of the process.
	if held := len(slots); held != 1 {
		t.Errorf("%d slots held, want only the one the test holds", held)
	}
}

// The mirror of the above: a run parked on the pool with no failure must still
// complete every step once slots free up, rather than deciding it is done.
func TestParkedRunResumesWhenSlotsFree(t *testing.T) {
	var started atomic.Int64
	plan := compile(t, threeRoots, func(reg *node.Registry) {
		reg.MustRegister(node.Static("counted.boom", node.NewSource("counted.boom",
			func(context.Context, *node.ExecutionContext) node.Result[*box] {
				started.Add(1)
				return node.Ok(&box{N: 1})
			})))
	})

	slots := NewSlots(1)
	ex := Run(t.Context(), plan, Options{Slots: slots})

	if ex.Failed() {
		t.Fatalf("run failed: %v", ex.Err())
	}
	if n := started.Load(); n != 3 {
		t.Errorf("%d steps started, want all 3 even though the pool held 1", n)
	}
	if held := len(slots); held != 0 {
		t.Errorf("%d slots still held, want 0", held)
	}
}

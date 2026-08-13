package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
)

const inputPipeline = `
version: 1
inputs:
  seed: Box
steps:
  bumped:
    uses: bump
    needs: [seed]
`

func TestSuppliedInputReachesTheStepThatNeedsIt(t *testing.T) {
	plan := compile(t, inputPipeline, nil)

	ex := Run(context.Background(), plan, Options{
		Inputs: map[string]node.Value{"seed": node.NewValue(&box{N: 41})},
	})

	if ex.Failed() {
		t.Fatalf("run failed: %v", ex.Err())
	}
	i, _ := plan.StepIndex("bumped")
	if got := ex.Steps[i].Value.Interface().(*box).N; got != 42 {
		t.Errorf("bumped = %d, want 42: the supplied value should have reached it", got)
	}
}

// One plan is a function of its inputs, not a constant: the same compiled plan
// run twice with different values produces different answers.
func TestOnePlanServesManyRuns(t *testing.T) {
	plan := compile(t, inputPipeline, nil)
	ctx := context.Background()
	i, _ := plan.StepIndex("bumped")

	first := Run(ctx, plan, Options{Inputs: map[string]node.Value{"seed": node.NewValue(&box{N: 1})}})
	second := Run(ctx, plan, Options{Inputs: map[string]node.Value{"seed": node.NewValue(&box{N: 100})}})

	if first.Failed() || second.Failed() {
		t.Fatal("both runs should succeed")
	}
	a := first.Steps[i].Value.Interface().(*box).N
	b := second.Steps[i].Value.Interface().(*box).N
	if a != 2 || b != 101 {
		t.Errorf("results = %d and %d, want 2 and 101", a, b)
	}
}

// A missing input fails that input's step, which stops the run before any node
// executes and leaves everything downstream skipped.
func TestMissingInputFailsBeforeAnythingRuns(t *testing.T) {
	plan := compile(t, inputPipeline, nil)

	ex := Run(context.Background(), plan, Options{})

	if !ex.Failed() {
		t.Fatal("expected a missing input to fail the run")
	}
	seed, _ := plan.StepIndex("seed")
	if ex.Steps[seed].State != StateFailed {
		t.Errorf("the input's state is %v, want failed", ex.Steps[seed].State)
	}
	if ex.Steps[seed].Err.Code != "input_missing" {
		t.Errorf("Code = %q, want %q", ex.Steps[seed].Err.Code, "input_missing")
	}
	bumped, _ := plan.StepIndex("bumped")
	if ex.Steps[bumped].State != StateSkipped {
		t.Errorf("bumped is %v, want skipped: no node should have run", ex.Steps[bumped].State)
	}
}

// The compiler proved every consumer expects the declared type. Checking the
// supplied value is what makes that proof hold for a value it never saw.
func TestIllTypedInputIsRejected(t *testing.T) {
	plan := compile(t, inputPipeline, nil)

	ex := Run(context.Background(), plan, Options{
		Inputs: map[string]node.Value{"seed": node.NewValue(&label{S: "wrong"})},
	})

	if !ex.Failed() {
		t.Fatal("expected an ill-typed input to fail the run")
	}
	seed, _ := plan.StepIndex("seed")
	if code := ex.Steps[seed].Err.Code; code != "input_type" {
		t.Errorf("Code = %q, want %q", code, "input_type")
	}
	if msg := ex.Steps[seed].Err.Message; !strings.Contains(msg, "Box") || !strings.Contains(msg, "Label") {
		t.Errorf("Message = %q, want it to name both types", msg)
	}
}

// An input that is supplied is reported like any other step, so a reader can
// see what a run was given.
func TestSuppliedInputIsReportedAsSucceeded(t *testing.T) {
	plan := compile(t, inputPipeline, nil)

	ex := Run(context.Background(), plan, Options{
		Inputs: map[string]node.Value{"seed": node.NewValue(&box{N: 1})},
	})

	seed, _ := plan.StepIndex("seed")
	if ex.Steps[seed].State != StateSucceeded {
		t.Errorf("the input's state is %v, want succeeded", ex.Steps[seed].State)
	}
}

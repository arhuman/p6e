package pipeline

import (
	"strings"
	"testing"
)

// An input is a graph node like any other, so a step binds it with needs and
// the compiler checks the edge.
func TestInputCompilesAsAGraphNode(t *testing.T) {
	plan := mustCompile(t, `
version: 1
inputs:
  seed: Alpha
steps:
  bumped:
    uses: alpha.bump
    needs: [seed]
`)

	if len(plan.Inputs) != 1 {
		t.Fatalf("plan declares %d inputs, want 1", len(plan.Inputs))
	}
	in := plan.Inputs[0]
	if in.Name != "seed" || in.Type != "Alpha" {
		t.Errorf("input = %q %s, want \"seed\" Alpha", in.Name, in.Type)
	}
	if !plan.Steps[in.Step].IsInput() {
		t.Error("the input's step should report IsInput")
	}

	bumped, _ := plan.StepIndex("bumped")
	if deps := plan.Steps[bumped].Deps; len(deps) != 1 || deps[0] != in.Step {
		t.Errorf("bumped deps = %v, want the input at %d", deps, in.Step)
	}
}

// An input carries no computation, so it is not something the executor
// schedules: it is recorded before anything starts.
func TestInputsAreNotRoots(t *testing.T) {
	plan := mustCompile(t, `
version: 1
inputs:
  seed: Alpha
steps:
  bumped:
    uses: alpha.bump
    needs: [seed]
`)

	for _, root := range plan.Roots {
		if plan.Steps[root].IsInput() {
			t.Errorf("input %q must not be a root", plan.Steps[root].ID)
		}
	}
}

// Inputs lead the plan and each group is sorted, so one file always produces
// one plan.
func TestInputsLeadThePlanInSortedOrder(t *testing.T) {
	plan := mustCompile(t, `
version: 1
inputs:
  zebra: Alpha
  apple: Alpha
steps:
  bumped:
    uses: alpha.bump
    needs: [apple]
`)

	if got := []string{plan.Steps[0].ID, plan.Steps[1].ID, plan.Steps[2].ID}; got[0] != "apple" ||
		got[1] != "zebra" || got[2] != "bumped" {
		t.Errorf("plan order = %v, want inputs sorted first then steps", got)
	}
}

// The edge check treats a supplied value exactly as it treats a computed one.
func TestInputEdgeIsTypeChecked(t *testing.T) {
	got := problems(t, `
version: 1
inputs:
  seed: Beta
steps:
  bumped:
    uses: alpha.bump
    needs: [seed]
`)

	if !strings.Contains(got, `pipeline input "seed" supplies Beta`) {
		t.Errorf("want the mismatch attributed to the input, got:\n%s", got)
	}
}

// A type is a name in a registry, so a typo is caught when the pipeline
// compiles rather than on the first run that supplies a value.
func TestInputTypeMustBeRegistered(t *testing.T) {
	got := problems(t, `
version: 1
inputs:
  seed: Alfa
steps:
  bumped:
    uses: alpha.bump
    needs: [seed]
`)

	if !strings.Contains(got, "not a registered type") {
		t.Errorf("want an unregistered type reported, got:\n%s", got)
	}
}

// Inputs and steps share one namespace, because needs cannot say which it
// meant.
func TestInputCannotCollideWithAStep(t *testing.T) {
	_, err := Parse(strings.NewReader(`
version: 1
inputs:
  seed: Alpha
steps:
  seed:
    uses: make.alpha
`))

	if err == nil {
		t.Fatal("expected a name shared by an input and a step to be rejected")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("error %q should say the names collide", err)
	}
}

func TestInputRequiresAType(t *testing.T) {
	_, err := Parse(strings.NewReader(`
version: 1
inputs:
  seed:
steps:
  source:
    uses: make.alpha
`))

	if err == nil {
		t.Fatal("expected an input with no type to be rejected")
	}
	if !strings.Contains(err.Error(), "missing type") {
		t.Errorf("error %q should say the type is missing", err)
	}
}

// A pipeline that declares nothing keeps compiling exactly as it did.
func TestPipelineWithoutInputsIsUnchanged(t *testing.T) {
	plan := mustCompile(t, `
version: 1
steps:
  source:
    uses: make.alpha
  bumped:
    uses: alpha.bump
    needs: [source]
`)

	if len(plan.Inputs) != 0 {
		t.Errorf("plan declares %d inputs, want none", len(plan.Inputs))
	}
	if len(plan.Roots) != 1 {
		t.Errorf("roots = %v, want the one source", plan.Roots)
	}
}

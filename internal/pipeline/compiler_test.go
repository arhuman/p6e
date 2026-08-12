package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
)

type alpha struct{ N int }

type beta struct{ S string }

func init() {
	node.RegisterType[*alpha]("Alpha")
	node.RegisterType[*beta]("Beta")
}

type pickyConfig struct {
	Mode string `yaml:"mode"`
}

// testRegistry provides just enough shapes to exercise every compile check:
// a source, a straight edge, a converter, a fan-in, and a node that rejects
// its configuration.
func testRegistry(t *testing.T) *node.Registry {
	t.Helper()
	r := node.NewRegistry()

	r.MustRegister(node.Static("make.alpha", node.NewSource("make.alpha",
		func(context.Context, *node.ExecutionContext) node.Result[*alpha] {
			return node.Ok(&alpha{N: 1})
		})))

	r.MustRegister(node.Static("alpha.bump", node.NewTypedNode("alpha.bump",
		func(_ context.Context, _ *node.ExecutionContext, a *alpha) node.Result[*alpha] {
			return node.Ok(&alpha{N: a.N + 1})
		})))

	r.MustRegister(node.Static("alpha.to.beta", node.NewTypedNode("alpha.to.beta",
		func(_ context.Context, _ *node.ExecutionContext, a *alpha) node.Result[*beta] {
			return node.Ok(&beta{S: "beta"})
		})))

	r.MustRegister(node.Static("join", node.NewTypedNode2("join",
		func(_ context.Context, _ *node.ExecutionContext, a *alpha, b *beta) node.Result[*beta] {
			return node.Ok(&beta{S: b.S})
		})))

	// Two ports of the same type: the shape where positional binding type
	// checks either way round and a swap is therefore silent.
	r.MustRegister(node.Static("pair", node.NewTypedNode2("pair",
		func(_ context.Context, _ *node.ExecutionContext, first *alpha, second *alpha) node.Result[*beta] {
			return node.Ok(&beta{S: "paired"})
		})))

	r.MustRegister(node.Definition{
		Name: "picky",
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c pickyConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, err
			}
			if c.Mode != "on" && c.Mode != "off" {
				return nil, node.Errf(node.KindInvalidInput, "bad_mode", "mode must be on or off, got %q", c.Mode)
			}
			return node.NewSource("picky", func(context.Context, *node.ExecutionContext) node.Result[*alpha] {
				return node.Ok(&alpha{})
			}), nil
		},
	})

	return r
}

func compileString(t *testing.T, src string) (*ExecutionPlan, error) {
	t.Helper()
	f, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return Compile(f, testRegistry(t), "test")
}

func mustCompile(t *testing.T, src string) *ExecutionPlan {
	t.Helper()
	p, err := compileString(t, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return p
}

// problems returns the compile error's messages, or fails if compilation
// unexpectedly succeeded.
func problems(t *testing.T, src string) string {
	t.Helper()
	_, err := compileString(t, src)
	if err == nil {
		t.Fatal("expected compilation to fail")
	}
	compileErr, ok := err.(*CompileError)
	if !ok {
		t.Fatalf("err is %T, want *CompileError", err)
	}
	var sb strings.Builder
	for _, p := range compileErr.Problems {
		sb.WriteString(p.String())
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestCompileProducesResolvedPlan(t *testing.T) {
	p := mustCompile(t, `
version: 1
steps:
  source:
    uses: make.alpha
  bump:
    uses: alpha.bump
    needs: [source]
  convert:
    uses: alpha.to.beta
    needs: [source]
`)

	// Steps are ordered by ID so a file always compiles to the same plan.
	want := []string{"bump", "convert", "source"}
	for i, id := range want {
		if p.Steps[i].ID != id {
			t.Fatalf("step %d is %q, want %q", i, p.Steps[i].ID, id)
		}
	}

	if len(p.Roots) != 1 || p.Steps[p.Roots[0]].ID != "source" {
		t.Errorf("Roots = %v, want just source", p.Roots)
	}

	source, _ := p.StepIndex("source")
	if len(p.Steps[source].Dependents) != 2 {
		t.Errorf("source has %d dependents, want 2", len(p.Steps[source].Dependents))
	}
	if len(p.Steps[source].Deps) != 0 {
		t.Errorf("source has %d deps, want 0", len(p.Steps[source].Deps))
	}

	bump, _ := p.StepIndex("bump")
	if len(p.Steps[bump].Deps) != 1 || p.Steps[bump].Deps[0] != source {
		t.Errorf("bump deps = %v, want [%d]", p.Steps[bump].Deps, source)
	}
}

// needs is positional (ADR 0002), so the plan must preserve its order.
func TestCompileBindsInputsPositionally(t *testing.T) {
	p := mustCompile(t, `
version: 1
steps:
  source:
    uses: make.alpha
  convert:
    uses: alpha.to.beta
    needs: [source]
  joined:
    uses: join
    needs: [source, convert]
`)

	joined, _ := p.StepIndex("joined")
	source, _ := p.StepIndex("source")
	convert, _ := p.StepIndex("convert")

	deps := p.Steps[joined].Deps
	if len(deps) != 2 || deps[0] != source || deps[1] != convert {
		t.Errorf("joined deps = %v, want [%d %d] in needs order", deps, source, convert)
	}
}

func TestCompileRejectsUnknownNode(t *testing.T) {
	got := problems(t, "version: 1\nsteps:\n  a:\n    uses: no.such.node\n")

	if !strings.Contains(got, "no.such.node") {
		t.Errorf("problems = %q, should name the unknown node", got)
	}
	if !strings.Contains(got, "make.alpha") {
		t.Errorf("problems = %q, should list the known nodes", got)
	}
}

func TestCompileRejectsMissingDependency(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  bump:
    uses: alpha.bump
    needs: [ghost]
`)

	if !strings.Contains(got, "ghost") {
		t.Errorf("problems = %q, should name the missing step", got)
	}
}

// The cycle report names the path, not just the fact that one exists.
func TestCompileRejectsCycle(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  a:
    uses: alpha.bump
    needs: [c]
  b:
    uses: alpha.bump
    needs: [a]
  c:
    uses: alpha.bump
    needs: [b]
`)

	if !strings.Contains(got, "cycle") {
		t.Fatalf("problems = %q, want a cycle report", got)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(got, `"`+id+`"`) {
			t.Errorf("cycle report %q should name step %q", got, id)
		}
	}
}

// This is the check the engine exists for: an incompatible edge fails before
// anything runs.
func TestCompileRejectsTypeMismatch(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  source:
    uses: make.alpha
  convert:
    uses: alpha.to.beta
    needs: [source]
  bump:
    uses: alpha.bump
    needs: [convert]
`)

	for _, want := range []string{`step "bump"`, "Alpha", `"convert"`, "Beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems = %q, should contain %q", got, want)
		}
	}
}

func TestCompileRejectsArityMismatch(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  source:
    uses: make.alpha
  joined:
    uses: join
    needs: [source]
`)

	if !strings.Contains(got, "2 input") || !strings.Contains(got, "needs lists 1") {
		t.Errorf("problems = %q, should report the arity mismatch", got)
	}
}

func TestCompileRejectsInvalidNodeConfig(t *testing.T) {
	got := problems(t, "version: 1\nsteps:\n  a:\n    uses: picky\n    with:\n      mode: sideways\n")

	if !strings.Contains(got, "sideways") {
		t.Errorf("problems = %q, should explain the rejected configuration", got)
	}
}

func TestCompileAcceptsValidNodeConfig(t *testing.T) {
	mustCompile(t, "version: 1\nsteps:\n  a:\n    uses: picky\n    with:\n      mode: on\n")
}

// Fixing one error at a time, recompiling between each, is a miserable way to
// write a pipeline.
func TestCompileReportsEveryProblemInAPhase(t *testing.T) {
	_, err := compileString(t, `
version: 1
steps:
  a:
    uses: no.such.node
  b:
    uses: also.missing
  c:
    uses: alpha.bump
    needs: [ghost]
`)

	compileErr, ok := err.(*CompileError)
	if !ok {
		t.Fatalf("err is %T, want *CompileError", err)
	}
	if len(compileErr.Problems) != 3 {
		t.Errorf("got %d problems, want 3: %v", len(compileErr.Problems), compileErr.Problems)
	}
}

func TestCompileCarriesRetryPolicy(t *testing.T) {
	p := mustCompile(t, `
version: 1
steps:
  source:
    uses: make.alpha
    retry:
      max_attempts: 4
      backoff: 10ms
`)

	if got := p.Steps[0].Retry.MaxAttempts; got != 4 {
		t.Errorf("MaxAttempts = %d, want 4", got)
	}
}

// The named form binds by input port name, so the order the mapping happens to
// be written in carries no meaning.
func TestCompileBindsNeedsByPortName(t *testing.T) {
	p := mustCompile(t, `
version: 1
steps:
  source:
    uses: make.alpha
  convert:
    uses: alpha.to.beta
    needs: [source]
  joined:
    uses: join
    needs:
      in1: convert
      in0: source
`)

	joined, _ := p.StepIndex("joined")
	source, _ := p.StepIndex("source")
	convert, _ := p.StepIndex("convert")

	deps := p.Steps[joined].Deps
	if len(deps) != 2 || deps[0] != source || deps[1] != convert {
		t.Errorf("joined deps = %v, want [%d %d]: in0 then in1, not mapping order",
			deps, source, convert)
	}
}

// This is the failure ADR 0005 exists to prevent. With positional binding both
// orders type check and mean different things; with named binding the meaning
// is fixed by the port name.
func TestNamedBindingIsImmuneToSwapping(t *testing.T) {
	first := mustCompile(t, `
version: 1
steps:
  left:
    uses: make.alpha
  right:
    uses: alpha.bump
    needs: [left]
  paired:
    uses: pair
    needs:
      in0: left
      in1: right
`)
	swapped := mustCompile(t, `
version: 1
steps:
  left:
    uses: make.alpha
  right:
    uses: alpha.bump
    needs: [left]
  paired:
    uses: pair
    needs:
      in1: right
      in0: left
`)

	a, _ := first.StepIndex("paired")
	b, _ := swapped.StepIndex("paired")
	if first.Steps[a].Deps[0] != swapped.Steps[b].Deps[0] || first.Steps[a].Deps[1] != swapped.Steps[b].Deps[1] {
		t.Errorf("writing the mapping in a different order changed the binding: %v against %v",
			first.Steps[a].Deps, swapped.Steps[b].Deps)
	}

	// The positional equivalent is not merely worse, it is rejected: a node
	// whose ports share a type can only be bound by name (ADR 0009).
	got := problems(t, `
version: 1
steps:
  left:
    uses: make.alpha
  right:
    uses: alpha.bump
    needs: [left]
  paired:
    uses: pair
    needs: [right, left]
`)
	if !strings.Contains(got, `bind needs by name instead`) {
		t.Errorf("positional binding of same-typed ports should be rejected, got:\n%s", got)
	}
}

// The rule is about ambiguity, not arity: a fan-in whose ports have distinct
// types is still bindable positionally, because the type check catches a swap.
func TestPositionalBindingSurvivesDistinctTypes(t *testing.T) {
	mustCompile(t, `
version: 1
steps:
  source:
    uses: make.alpha
  convert:
    uses: alpha.to.beta
    needs: [source]
  joined:
    uses: join
    needs: [source, convert]
`)
}

// A node with ambiguous ports needs the mapping form whatever the list says, so
// the count is not what gets reported.
func TestAmbiguousInputsReportedBeforeArity(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  left:
    uses: make.alpha
  paired:
    uses: pair
    needs: [left]
`)
	if !strings.Contains(got, `has inputs of identical type Alpha ("in0", "in1")`) {
		t.Errorf("want the ambiguity reported, got:\n%s", got)
	}
	if strings.Contains(got, "needs lists") {
		t.Errorf("want the ambiguity reported instead of the count, got:\n%s", got)
	}
}

func TestCompileRejectsUnboundInput(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  source:
    uses: make.alpha
  joined:
    uses: join
    needs:
      in0: source
`)

	if !strings.Contains(got, `"in1"`) || !strings.Contains(got, "not bound") {
		t.Errorf("problems = %q, should report that in1 is unbound", got)
	}
}

// A typo in a port name must fail rather than silently leave the input
// unconnected.
func TestCompileRejectsUnknownPortName(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  source:
    uses: make.alpha
  convert:
    uses: alpha.to.beta
    needs: [source]
  joined:
    uses: join
    needs:
      in0: source
      in2: convert
`)

	if !strings.Contains(got, `"in2"`) || !strings.Contains(got, "no such input") {
		t.Errorf("problems = %q, should reject the unknown port name", got)
	}
}

func TestNamedBindingTypeChecksPerPort(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  source:
    uses: make.alpha
  convert:
    uses: alpha.to.beta
    needs: [source]
  joined:
    uses: join
    needs:
      in0: convert
      in1: convert
`)

	if !strings.Contains(got, `input "in0" expects Alpha`) {
		t.Errorf("problems = %q, should report the mistyped port", got)
	}
}

func TestNeedsRejectsUnusableForms(t *testing.T) {
	cases := map[string]string{
		"scalar":     "version: 1\nsteps:\n  a:\n    uses: x\n    needs: fetch\n",
		"empty bind": "version: 1\nsteps:\n  a:\n    uses: x\n    needs:\n      in0: \"\"\n",
	}
	for name, src := range cases {
		if _, err := Parse(strings.NewReader(src)); err == nil {
			t.Errorf("%s: expected needs to be rejected", name)
		}
	}
}

// A typo in one step's with block used to hide a type error in another, because
// compilation returned after the first phase that found anything.
func TestCompileReportsProblemsFromDifferentPhasesTogether(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  source:
    uses: make.alpha
  misconfigured:
    uses: picky
    with:
      mode: sideways
  mistyped:
    uses: alpha.bump
    needs: [convert]
  convert:
    uses: alpha.to.beta
    needs: [source]
`)

	if !strings.Contains(got, "sideways") {
		t.Errorf("problems = %q, should report the bad configuration", got)
	}
	if !strings.Contains(got, `input "in" expects Alpha`) {
		t.Errorf("problems = %q, should also report the type error in another step", got)
	}
}

// A step naming a missing dependency must not also produce a bogus type error
// against whichever step happens to sit at index zero.
func TestMissingDependencyDoesNotProduceASpuriousTypeError(t *testing.T) {
	got := problems(t, `
version: 1
steps:
  bump:
    uses: alpha.bump
    needs: [ghost]
`)

	if strings.Contains(got, "expects") {
		t.Errorf("problems = %q, should report only the missing dependency", got)
	}
}

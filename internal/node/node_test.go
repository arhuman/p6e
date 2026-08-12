package node

import (
	"context"
	"strings"
	"testing"
)

type payload struct{ N int }

type other struct{ S string }

func init() {
	RegisterType[*payload]("Payload")
	RegisterType[*other]("Other")
}

func bump(_ context.Context, _ *ExecutionContext, p *payload) Result[*payload] {
	return Ok(&payload{N: p.N + 1})
}

func testEC() *ExecutionContext {
	return &ExecutionContext{WorkflowID: "w", ExecutionID: "e", StepID: "s", Attempt: 1}
}

func TestTypedNodeDescriptor(t *testing.T) {
	d := NewTypedNode("bump", bump).Descriptor()

	if d.Name != "bump" {
		t.Errorf("Name = %q, want %q", d.Name, "bump")
	}
	if d.Arity() != 1 {
		t.Fatalf("Arity = %d, want 1", d.Arity())
	}
	if d.Inputs[0].Type != "Payload" {
		t.Errorf("input type = %q, want %q", d.Inputs[0].Type, "Payload")
	}
	if d.Output.Type != "Payload" {
		t.Errorf("output type = %q, want %q", d.Output.Type, "Payload")
	}
}

func TestTypedNodeExecute(t *testing.T) {
	n := NewTypedNode("bump", bump)

	r := n.Execute(context.Background(), testEC(), []Value{NewValue(&payload{N: 1})})

	if r.Failed() {
		t.Fatalf("Execute failed: %v", r.Err)
	}
	if r.Value.Type() != "Payload" {
		t.Errorf("output TypeID = %q, want %q", r.Value.Type(), "Payload")
	}
	got, ok := r.Value.Interface().(*payload)
	if !ok {
		t.Fatalf("output holds %T, want *payload", r.Value.Interface())
	}
	if got.N != 2 {
		t.Errorf("N = %d, want 2", got.N)
	}
}

// The compiler is supposed to make this unreachable. It is still checked, and
// reported as internal rather than invalid_input: a mistyped edge that reaches
// execution is an engine bug, not the workflow author's mistake.
func TestTypedNodeRejectsWrongInputType(t *testing.T) {
	n := NewTypedNode("bump", bump)

	r := n.Execute(context.Background(), testEC(), []Value{NewValue(&other{S: "x"})})

	if !r.Failed() {
		t.Fatal("expected a failure on a mistyped input")
	}
	if r.Err.Kind != KindInternal {
		t.Errorf("Kind = %q, want %q", r.Err.Kind, KindInternal)
	}
	if !strings.Contains(r.Err.Message, "Payload") || !strings.Contains(r.Err.Message, "Other") {
		t.Errorf("message %q should name both the expected and received types", r.Err.Message)
	}
}

func TestTypedNodeRejectsWrongArity(t *testing.T) {
	n := NewTypedNode("bump", bump)

	r := n.Execute(context.Background(), testEC(), nil)

	if !r.Failed() {
		t.Fatal("expected a failure on missing input")
	}
	if r.Err.Code != "arity_mismatch" {
		t.Errorf("Code = %q, want %q", r.Err.Code, "arity_mismatch")
	}
}

func TestTypedNodePropagatesNodeError(t *testing.T) {
	n := NewTypedNode("boom", func(context.Context, *ExecutionContext, *payload) Result[*payload] {
		return Fail[*payload](Errf(KindTransient, "boom", "exploded"))
	})

	r := n.Execute(context.Background(), testEC(), []Value{NewValue(&payload{})})

	if !r.Failed() {
		t.Fatal("expected a failure")
	}
	if r.Err.Code != "boom" || !r.Err.Retryable {
		t.Errorf("error = %+v, want a retryable boom", r.Err)
	}
	if !r.Value.IsZero() {
		t.Error("a failed result must not carry a value")
	}
}

func TestSourceNode(t *testing.T) {
	n := NewSource("const", func(context.Context, *ExecutionContext) Result[*payload] {
		return Ok(&payload{N: 42})
	})

	if n.Descriptor().Arity() != 0 {
		t.Fatalf("Arity = %d, want 0", n.Descriptor().Arity())
	}
	r := n.Execute(context.Background(), testEC(), nil)
	if r.Failed() {
		t.Fatalf("Execute failed: %v", r.Err)
	}
	if r.Value.Interface().(*payload).N != 42 {
		t.Error("source did not produce its constant")
	}
}

func TestTypedNode2BindsInputsPositionally(t *testing.T) {
	n := NewTypedNode2("join", func(_ context.Context, _ *ExecutionContext, p *payload, o *other) Result[*other] {
		return Ok(&other{S: o.S + ":" + string(rune('0'+p.N))})
	})

	d := n.Descriptor()
	if d.Arity() != 2 {
		t.Fatalf("Arity = %d, want 2", d.Arity())
	}
	if d.Inputs[0].Type != "Payload" || d.Inputs[1].Type != "Other" {
		t.Fatalf("input types = %s, want (Payload, Other)", d.InputTypes())
	}

	r := n.Execute(context.Background(), testEC(), []Value{
		NewValue(&payload{N: 7}),
		NewValue(&other{S: "x"}),
	})
	if r.Failed() {
		t.Fatalf("Execute failed: %v", r.Err)
	}
	if got := r.Value.Interface().(*other).S; got != "x:7" {
		t.Errorf("S = %q, want %q", got, "x:7")
	}
}

// Fan-out hands the same Value to several steps. Nothing may copy the payload:
// that is the property that keeps large values cheap.
func TestValueIsSharedNotCopied(t *testing.T) {
	p := &payload{N: 1}
	v := NewValue(p)

	if v.Interface().(*payload) != p {
		t.Error("Value copied its payload")
	}
}

func TestZeroValue(t *testing.T) {
	var v Value
	if !v.IsZero() {
		t.Error("the zero Value should report IsZero")
	}
	if NewValue(&payload{}).IsZero() {
		t.Error("a populated Value should not report IsZero")
	}
}

// The port of ADR 0001's winning design must keep its numbers: one interface
// dispatch, one assertion, no allocation.
func BenchmarkTypedNodeExecute(b *testing.B) {
	n := NewTypedNodeReuse()
	ctx := context.Background()
	ec := testEC()
	in := []Value{NewValue(&payload{N: 1})}

	b.ReportAllocs()
	for b.Loop() {
		r := n.Execute(ctx, ec, in)
		if r.Failed() {
			b.Fatal(r.Err)
		}
	}
}

// NewTypedNodeReuse builds a node whose body allocates nothing, so the
// benchmark measures the adapter rather than the node.
func NewTypedNodeReuse() RuntimeNode {
	out := &payload{N: 2}
	return NewTypedNode("noop", func(context.Context, *ExecutionContext, *payload) Result[*payload] {
		return Ok(out)
	})
}

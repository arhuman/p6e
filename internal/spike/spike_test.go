package spike

import (
	"context"
	"testing"
)

// The candidates must agree: same chain, same answer. Otherwise the benchmark
// is comparing different amounts of work.

const chainLen = 5

func TestCandidatesAgree(t *testing.T) {
	ctx := context.Background()
	in := Payload{N: 1, Data: []byte("x")}
	boxed := Value{Type: "Payload", ptr: in}
	want := int64(1 + chainLen)

	baseline, err := RunBaseline(ctx, BuildBaselineChain(chainLen), in)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if baseline.N != want {
		t.Errorf("baseline N = %d, want %d", baseline.N, want)
	}

	erased, err := RunErased(ctx, BuildErasedChain(chainLen), boxed)
	if err != nil {
		t.Fatalf("erased: %v", err)
	}
	if got := erased.ptr.(Payload).N; got != want {
		t.Errorf("erased N = %d, want %d", got, want)
	}

	reflected, err := RunReflect(ctx, BuildReflectChain(chainLen), in)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if got := reflected.(Payload).N; got != want {
		t.Errorf("reflect N = %d, want %d", got, want)
	}

	r := BuildComposedChain(chainLen)(ctx, boxed)
	if r.Err != nil {
		t.Fatalf("composed: %v", r.Err)
	}
	if got := r.Value.ptr.(Payload).N; got != want {
		t.Errorf("composed N = %d, want %d", got, want)
	}
}

// A mistyped edge must be caught, and the candidates differ in when: erased and
// reflect fail at execution, composition fails at build. That difference is the
// architectural point, so pin it.

func TestErasedRejectsWrongTypeAtRuntime(t *testing.T) {
	chain := BuildErasedChain(1)
	_, err := RunErased(context.Background(), chain, Value{Type: "String", ptr: "not a payload"})
	if err == nil {
		t.Fatal("expected a type error at execution")
	}
}

func TestComposeRejectsWrongTypeAtBuild(t *testing.T) {
	mismatched := []ComposableNode{
		NewComposableNode("Payload", "String", func(_ context.Context, p Payload) Result[string] {
			return Result[string]{Value: "converted"}
		}),
		NewComposableNode("Payload", "Payload", bump),
	}
	if _, err := Compose(mismatched); err == nil {
		t.Fatal("expected a type error at compose time")
	}
}

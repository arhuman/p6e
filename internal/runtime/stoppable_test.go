package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
)

const oneHang = `
version: 1
steps:
  a:
    uses: hang
`

// A node that claims to honour cancellation and then does not has broken a
// contract it opted into. That is a bug in the node, not the documented
// limitation of ADR 0004, and the run says which it is looking at.
//
// The deadline is identical either way: the claim buys a diagnostic, never a
// longer wait, because Go cannot verify it.
func TestAbandonedStoppableNodeIsReportedAsABrokenPromise(t *testing.T) {
	plan := compile(t, oneHang, func(reg *node.Registry) {
		// Declares stoppable, then ignores its context entirely: the liar.
		reg.MustRegister(node.Static("hang", node.AsStoppable(node.NewSource("hang",
			func(ctx context.Context, _ *node.ExecutionContext) node.Result[*box] {
				<-make(chan struct{})
				return node.Ok(&box{})
			}))))
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ex := Run(ctx, plan, Options{AbandonAfter: 20 * time.Millisecond})

	if ex.Abandoned != 1 {
		t.Fatalf("Abandoned = %d, want 1", ex.Abandoned)
	}
	err := ex.Steps[0].Err
	if err == nil {
		t.Fatal("an abandoned step must carry an error")
	}
	if err.Code != "broken_cancellation" {
		t.Errorf("Code = %q, want %q", err.Code, "broken_cancellation")
	}
	if err.Kind != node.KindInternal {
		t.Errorf("Kind = %q, want %q: a broken promise is the node's bug", err.Kind, node.KindInternal)
	}
	if !strings.Contains(err.Message, "honours cancellation") {
		t.Errorf("Message = %q, want it to name the promise that was broken", err.Message)
	}
}

// A node that never promised anything is reported exactly as before. Nothing
// about the existing contract changed for it.
func TestAbandonedOrdinaryNodeIsUnchanged(t *testing.T) {
	plan := compile(t, oneHang, func(reg *node.Registry) {
		reg.MustRegister(hangingSource())
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ex := Run(ctx, plan, Options{AbandonAfter: 20 * time.Millisecond})

	if ex.Abandoned != 1 {
		t.Fatalf("Abandoned = %d, want 1", ex.Abandoned)
	}
	if code := ex.Steps[0].Err.Code; code != "abandoned" {
		t.Errorf("Code = %q, want %q", code, "abandoned")
	}
	if kind := ex.Steps[0].Err.Kind; kind != node.KindCancelled {
		t.Errorf("Kind = %q, want %q", kind, node.KindCancelled)
	}
}

// AsStoppable must not change what a node does, only what it says about itself.
func TestAsStoppablePreservesBehaviour(t *testing.T) {
	inner := node.NewSource("plain", func(context.Context, *node.ExecutionContext) node.Result[*box] {
		return node.Ok(&box{N: 7})
	})
	wrapped := node.AsStoppable(inner)

	if !node.HonoursCancellation(wrapped) {
		t.Error("a wrapped node must report that it honours cancellation")
	}
	if node.HonoursCancellation(inner) {
		t.Error("an unwrapped node must not claim anything")
	}
	if wrapped.Descriptor().Name != inner.Descriptor().Name {
		t.Errorf("Descriptor changed: %q became %q",
			inner.Descriptor().Name, wrapped.Descriptor().Name)
	}

	got := wrapped.Execute(t.Context(), &node.ExecutionContext{}, nil)
	if got.Failed() {
		t.Fatalf("Execute failed: %v", got.Err)
	}
	if b, ok := got.Value.Interface().(*box); !ok || b.N != 7 {
		t.Errorf("Execute returned %+v, want the inner node's value", got.Value.Interface())
	}
}

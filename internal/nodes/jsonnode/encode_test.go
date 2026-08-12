package jsonnode

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func encodeDoc(t *testing.T, root any) node.ResultValue {
	t.Helper()
	n, err := EncodeDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := []node.Value{node.NewValue(&types.Document{Root: root})}
	return n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
}

func TestEncodeProducesBytes(t *testing.T) {
	r := encodeDoc(t, map[string]any{"user": "ada"})

	if r.Failed() {
		t.Fatalf("encode: %v", r.Err)
	}
	if got := string(r.Value.Interface().(*types.Bytes).Value); got != `{"user":"ada"}` {
		t.Errorf("encoded %s, want %s", got, `{"user":"ada"}`)
	}
}

// decode then encode is the round trip a pipeline makes when it reads a
// document, and sends it on.
func TestEncodeRoundTripsWithDecode(t *testing.T) {
	const src = `{"active":true,"count":3,"user":{"name":"ada"}}`

	root := mustDecode(t, src)
	r := encodeDoc(t, root)

	if r.Failed() {
		t.Fatalf("encode: %v", r.Err)
	}
	if got := string(r.Value.Interface().(*types.Bytes).Value); got != src {
		t.Errorf("round trip produced %s, want %s", got, src)
	}
}

// A document from json.decode always encodes, but one another node assembled
// need not, so the failure is reported rather than assumed away.
func TestEncodeRejectsAnUnencodableDocument(t *testing.T) {
	r := encodeDoc(t, math.Inf(1))

	if !r.Failed() {
		t.Fatal("expected an infinite float to fail encoding")
	}
	if r.Err.Kind != node.KindInvalidInput {
		t.Errorf("Kind = %q, want %q", r.Err.Kind, node.KindInvalidInput)
	}
	if r.Err.Retryable {
		t.Error("the same value fails the same way, so it must not be retryable")
	}
}

func TestEncodeDeclaresDocumentToBytes(t *testing.T) {
	n, err := EncodeDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := n.Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "JSONDocument" {
		t.Errorf("inputs = %s, want (JSONDocument)", d.InputTypes())
	}
	if d.Output.Type != "Bytes" {
		t.Errorf("output type = %q, want %q", d.Output.Type, "Bytes")
	}
}

func TestEncodeRejectsAConfiguration(t *testing.T) {
	_, err := EncodeDefinition().New(withBlock("indent: true\n"))

	if err == nil {
		t.Fatal("expected a with block to be rejected")
	}
	if !strings.Contains(err.Error(), "indent") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

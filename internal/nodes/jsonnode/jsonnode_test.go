package jsonnode

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"gopkg.in/yaml.v3"
)

// withBlock mirrors what the pipeline package hands a node: a with block
// decoded strictly, so a typo is an error rather than a silent default.
type withBlock string

func (w withBlock) Decode(dst any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(w)))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func build(t *testing.T) node.RuntimeNode {
	t.Helper()
	n, err := DecodeDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n
}

func decodeJSON(t *testing.T, src string) node.ResultValue {
	t.Helper()
	in := []node.Value{node.NewValue(&types.Bytes{Value: []byte(src)})}
	return build(t).Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
}

func mustDecode(t *testing.T, src string) any {
	t.Helper()
	r := decodeJSON(t, src)
	if r.Failed() {
		t.Fatalf("decoding %s: %v", src, r.Err)
	}
	doc, ok := r.Value.Interface().(*types.Document)
	if !ok {
		t.Fatalf("output holds %T, want *types.Document", r.Value.Interface())
	}
	return doc.Root
}

func TestDecodeProducesADocument(t *testing.T) {
	root := mustDecode(t, `{"user":{"name":"ada"},"active":true}`)

	want := map[string]any{
		"user":   map[string]any{"name": "ada"},
		"active": true,
	}
	if !reflect.DeepEqual(root, want) {
		t.Errorf("Root = %#v, want %#v", root, want)
	}
}

// Root is whatever the document was, so a pipeline can decode an API that
// answers with an array or a bare scalar.
func TestDecodeAcceptsANonObjectRoot(t *testing.T) {
	if root := mustDecode(t, `[1,2]`); !reflect.DeepEqual(root, []any{1.0, 2.0}) {
		t.Errorf("Root = %#v, want an array", root)
	}
	if root := mustDecode(t, `"bare"`); root != "bare" {
		t.Errorf("Root = %#v, want the scalar", root)
	}
}

// Bad bytes are the pipeline's problem, not the world's: retrying the same
// payload cannot turn it into JSON.
func TestDecodeRejectsMalformedJSON(t *testing.T) {
	r := decodeJSON(t, `{"user":`)

	if !r.Failed() {
		t.Fatal("expected truncated JSON to fail")
	}
	if r.Err.Kind != node.KindInvalidInput {
		t.Errorf("Kind = %q, want %q", r.Err.Kind, node.KindInvalidInput)
	}
	if r.Err.Retryable {
		t.Error("malformed JSON must not be retryable")
	}
	if r.Err.Cause == nil {
		t.Error("the underlying decode error should survive as the cause, for diagnostics")
	}
}

func TestDecodeReportsAFailureWithoutAValue(t *testing.T) {
	r := decodeJSON(t, `nope`)

	if !r.Failed() {
		t.Fatal("expected invalid JSON to fail")
	}
	if !r.Value.IsZero() {
		t.Error("a failed result must not carry a value")
	}
}

func TestDecodeDeclaresBytesToDocument(t *testing.T) {
	d := build(t).Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "Bytes" {
		t.Errorf("inputs = %s, want (Bytes)", d.InputTypes())
	}
	if d.Output.Type != "JSONDocument" {
		t.Errorf("output type = %q, want %q", d.Output.Type, "JSONDocument")
	}
}

// json.decode has nothing to configure, so a with block is a mistake worth
// catching at check time rather than ignoring.
func TestDecodeRejectsAConfiguration(t *testing.T) {
	_, err := DecodeDefinition().New(withBlock("path: user.name\n"))

	if err == nil {
		t.Fatal("expected a with block to be rejected")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

package value

import (
	"context"
	"errors"
	"io"
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

func build(t *testing.T, with string) node.RuntimeNode {
	t.Helper()
	n, err := Definition().New(withBlock(with))
	if err != nil {
		t.Fatalf("New(%q): %v", with, err)
	}
	return n
}

// rejected returns the configuration error's message, failing the test if the
// configuration was accepted instead.
func rejected(t *testing.T, with string) string {
	t.Helper()
	if _, err := Definition().New(withBlock(with)); err != nil {
		return err.Error()
	}
	t.Fatalf("New(%q) accepted an invalid configuration", with)
	return ""
}

func run(t *testing.T, n node.RuntimeNode) any {
	t.Helper()
	r := n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, nil)
	if r.Failed() {
		t.Fatalf("Execute: %v", r.Err)
	}
	return r.Value.Interface()
}

func TestValueProducesTheConfiguredConstant(t *testing.T) {
	got := run(t, build(t, "type: Text\nvalue: hello\n"))

	text, ok := got.(*types.Text)
	if !ok {
		t.Fatalf("output holds %T, want *types.Text", got)
	}
	if text.Value != "hello" {
		t.Errorf("Value = %q, want %q", text.Value, "hello")
	}
}

// Bytes is configured as a YAML string, since a pipeline file has no way to
// write a byte slice.
func TestValueTurnsAStringLiteralIntoBytes(t *testing.T) {
	got := run(t, build(t, "type: Bytes\nvalue: '{\"a\":1}'\n"))

	b, ok := got.(*types.Bytes)
	if !ok {
		t.Fatalf("output holds %T, want *types.Bytes", got)
	}
	if string(b.Value) != `{"a":1}` {
		t.Errorf("Value = %q, want %q", b.Value, `{"a":1}`)
	}
}

// This is the reason the type switch lives in New: the node's declared output
// type, which the compiler checks edges against, comes from its configuration.
func TestValueOutputTypeFollowsTheConfiguredType(t *testing.T) {
	for _, c := range []struct{ with, wantType string }{
		{"type: Bytes\nvalue: raw\n", "Bytes"},
		{"type: Text\nvalue: raw\n", "Text"},
		{"type: Bool\nvalue: true\n", "Bool"},
		{"type: Int\nvalue: 42\n", "Int"},
	} {
		d := build(t, c.with).Descriptor()
		if d.Output.Type != node.TypeID(c.wantType) {
			t.Errorf("output type = %q, want %q", d.Output.Type, c.wantType)
		}
		if d.Arity() != 0 {
			t.Errorf("%s value node has arity %d, want 0: it is a source", c.wantType, d.Arity())
		}
	}
}

func TestValueProducesSignedIntegers(t *testing.T) {
	got := run(t, build(t, "type: Int\nvalue: -7\n"))

	if n := got.(*types.Int).Value; n != -7 {
		t.Errorf("Value = %d, want -7", n)
	}
}

// The message has to list what is accepted, otherwise the author's next move is
// to go read the source.
func TestValueRejectsAnUnknownTypeName(t *testing.T) {
	got := rejected(t, "type: Blob\nvalue: x\n")

	if !strings.Contains(got, "Blob") {
		t.Errorf("error %q should name the rejected type", got)
	}
	for _, want := range []string{"Bytes", "Text", "Bool", "Int"} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q should list the accepted type %q", got, want)
		}
	}
}

func TestValueRejectsAMissingType(t *testing.T) {
	got := rejected(t, "value: x\n")

	if !strings.Contains(got, "type") {
		t.Errorf("error %q should say a type is required", got)
	}
}

// A source with no value would produce the zero payload, which is never what
// the author meant to write.
func TestValueRejectsAMissingValue(t *testing.T) {
	got := rejected(t, "type: Text\n")

	if !strings.Contains(got, "value") {
		t.Errorf("error %q should say a value is required", got)
	}
}

func TestValueRejectsALiteralThatDoesNotFitTheType(t *testing.T) {
	got := rejected(t, "type: Int\nvalue: not-a-number\n")

	if !strings.Contains(got, "Int") {
		t.Errorf("error %q should name the type the literal failed to satisfy", got)
	}
}

func TestValueRejectsAnUnknownConfigField(t *testing.T) {
	got := rejected(t, "type: Text\nvalue: x\ndefault: y\n")

	if !strings.Contains(got, "default") {
		t.Errorf("error %q should name the unknown field", got)
	}
}

// A configuration error is the pipeline author's mistake, not a condition in
// the world, so retrying is never the answer.
func TestValueConfigErrorsAreInvalidInput(t *testing.T) {
	_, err := Definition().New(withBlock("type: Blob\nvalue: x\n"))

	var ne *node.Error
	if !errors.As(err, &ne) {
		t.Fatalf("err is %T, want *node.Error", err)
	}
	if ne.Kind != node.KindInvalidInput {
		t.Errorf("Kind = %q, want %q", ne.Kind, node.KindInvalidInput)
	}
	if ne.Retryable {
		t.Error("a configuration error must not be retryable")
	}
}

// Every execution of a compiled plan shares one node, and the constant is built
// once: outputs are immutable, so nothing needs a fresh copy.
func TestValueSharesOneConstantAcrossExecutions(t *testing.T) {
	n := build(t, "type: Bytes\nvalue: payload\n")

	if run(t, n) != run(t, n) {
		t.Error("the value node reallocated its constant")
	}
}

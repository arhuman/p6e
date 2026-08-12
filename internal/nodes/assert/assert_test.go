package assert

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

type withBlock string

func (w withBlock) Decode(dst any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(w)))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func assertTrue(t *testing.T, cfg node.Config, verdict *types.Bool) node.ResultValue {
	t.Helper()
	n, err := TrueDefinition().New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := []node.Value{node.NewValue(verdict)}
	return n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
}

func TestTrueVerdictPassesThrough(t *testing.T) {
	verdict := &types.Bool{Value: true}

	r := assertTrue(t, node.EmptyConfig, verdict)

	if r.Failed() {
		t.Fatalf("a true verdict must not fail: %v", r.Err)
	}
	if r.Value.Interface().(*types.Bool) != verdict {
		t.Error("the verdict should pass through by reference, not be rebuilt")
	}
}

// This is the node's whole purpose: a false verdict stops the run, which is
// what gives the process a non-zero exit code for cron or CI to act on.
func TestFalseVerdictFails(t *testing.T) {
	r := assertTrue(t, node.EmptyConfig, &types.Bool{Value: false})

	if !r.Failed() {
		t.Fatal("a false verdict must fail the step")
	}
	if r.Err.Kind != node.KindPermanent {
		t.Errorf("Kind = %q, want %q", r.Err.Kind, node.KindPermanent)
	}
	if r.Err.Code != "assertion_failed" {
		t.Errorf("Code = %q, want %q", r.Err.Code, "assertion_failed")
	}
	if r.Err.Retryable {
		t.Error("re-testing the same verdict reaches the same answer, so it must not be retryable")
	}
	if !r.Value.IsZero() {
		t.Error("a failed result must not carry a value")
	}
}

// The message is what a person reads when the run fails, so it has to be able
// to say what was expected.
func TestConfiguredMessageIsReported(t *testing.T) {
	r := assertTrue(t, withBlock("message: service reported an unhealthy status\n"),
		&types.Bool{Value: false})

	if !r.Failed() {
		t.Fatal("expected the assertion to fail")
	}
	if !strings.Contains(r.Err.Message, "unhealthy status") {
		t.Errorf("Message = %q, want the configured text", r.Err.Message)
	}
}

func TestDefaultMessageExplainsItself(t *testing.T) {
	r := assertTrue(t, node.EmptyConfig, &types.Bool{Value: false})

	if !strings.Contains(r.Err.Message, "false") {
		t.Errorf("Message = %q, want it to say the verdict was false", r.Err.Message)
	}
}

func TestDeclaresBoolToBool(t *testing.T) {
	n, err := TrueDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := n.Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "Bool" {
		t.Errorf("inputs = %s, want (Bool)", d.InputTypes())
	}
	// The verdict passes through so a later step can depend on this one, which
	// is the only conditional execution available without engine support.
	if d.Output.Type != "Bool" {
		t.Errorf("output type = %q, want %q", d.Output.Type, "Bool")
	}
}

func TestRejectsAnUnknownField(t *testing.T) {
	_, err := TrueDefinition().New(withBlock("expect: false\n"))

	if err == nil {
		t.Fatal("expected an unknown field to be rejected")
	}
	if !strings.Contains(err.Error(), "expect") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

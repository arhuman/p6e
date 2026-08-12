package jsonnode

import (
	"context"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func get(t *testing.T, with string, root any) node.ResultValue {
	t.Helper()
	n, err := GetDefinition().New(withBlock(with))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := []node.Value{node.NewValue(&types.Document{Root: root})}
	return n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
}

func document() any {
	return map[string]any{
		"user":   map[string]any{"name": "ada", "admin": true},
		"count":  float64(3),
		"ratio":  1.5,
		"absent": nil,
	}
}

func TestGetReadsEachType(t *testing.T) {
	if r := get(t, "path: user.name\nas: Text\n", document()); r.Failed() {
		t.Errorf("Text: %v", r.Err)
	} else if v := r.Value.Interface().(*types.Text).Value; v != "ada" {
		t.Errorf("Text = %q, want %q", v, "ada")
	}

	if r := get(t, "path: user.admin\nas: Bool\n", document()); r.Failed() {
		t.Errorf("Bool: %v", r.Err)
	} else if v := r.Value.Interface().(*types.Bool).Value; !v {
		t.Error("Bool = false, want true")
	}

	if r := get(t, "path: count\nas: Int\n", document()); r.Failed() {
		t.Errorf("Int: %v", r.Err)
	} else if v := r.Value.Interface().(*types.Int).Value; v != 3 {
		t.Errorf("Int = %d, want 3", v)
	}

	if r := get(t, "path: user.name\nas: Bytes\n", document()); r.Failed() {
		t.Errorf("Bytes: %v", r.Err)
	} else if v := string(r.Value.Interface().(*types.Bytes).Value); v != "ada" {
		t.Errorf("Bytes = %q, want %q", v, "ada")
	}
}

// The declared type becomes the step's output type, which is what keeps
// extraction statically checked without a structural type system.
func TestGetOutputTypeFollowsTheDeclaredType(t *testing.T) {
	for as, want := range map[string]node.TypeID{
		"Text": "Text", "Bytes": "Bytes", "Bool": "Bool", "Int": "Int",
	} {
		n, err := GetDefinition().New(withBlock("path: x\nas: " + as + "\n"))
		if err != nil {
			t.Fatalf("as: %s: %v", as, err)
		}
		d := n.Descriptor()
		if d.Arity() != 1 || d.Inputs[0].Type != "JSONDocument" {
			t.Errorf("as: %s: inputs = %s, want (JSONDocument)", as, d.InputTypes())
		}
		if d.Output.Type != want {
			t.Errorf("as: %s: output = %q, want %q", as, d.Output.Type, want)
		}
	}
}

// Conversion is explicit: the engine never coerces one type into another, and
// this node is not an exception to that.
func TestGetDoesNotCoerceBetweenTypes(t *testing.T) {
	cases := []struct {
		name string
		with string
	}{
		{"number as text", "path: count\nas: Text\n"},
		{"text as int", "path: user.name\nas: Int\n"},
		{"object as text", "path: user\nas: Text\n"},
		{"null as text", "path: absent\nas: Text\n"},
		{"bool as int", "path: user.admin\nas: Int\n"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := get(t, c.with, document())
			if !r.Failed() {
				t.Fatal("expected the conversion to be refused")
			}
			if r.Err.Kind != node.KindInvalidInput || r.Err.Retryable {
				t.Errorf("Kind = %q retryable = %v, want invalid_input and not retryable",
					r.Err.Kind, r.Err.Retryable)
			}
		})
	}
}

// JSON has no integer type, so a whole number arrives as a float and is a valid
// Int; one with a fractional part is not.
func TestGetIntAcceptsWholeNumbersOnly(t *testing.T) {
	if r := get(t, "path: count\nas: Int\n", document()); r.Failed() {
		t.Errorf("3 is a whole number and should read as Int: %v", r.Err)
	}
	if r := get(t, "path: ratio\nas: Int\n", document()); !r.Failed() {
		t.Error("1.5 is not a whole number and must not read as Int")
	}
}

func TestGetAbsentPathWithoutADefaultFails(t *testing.T) {
	r := get(t, "path: user.nickname\nas: Text\n", document())

	if !r.Failed() {
		t.Fatal("expected an absent path with no default to fail")
	}
	if r.Err.Code != "path_absent" {
		t.Errorf("Code = %q, want %q", r.Err.Code, "path_absent")
	}
	if r.Err.Retryable {
		t.Error("the same document fails the same way, so it must not be retryable")
	}
}

func TestGetAbsentPathWithADefaultUsesIt(t *testing.T) {
	r := get(t, "path: user.nickname\nas: Text\ndefault: anonymous\n", document())

	if r.Failed() {
		t.Fatalf("a configured default should cover an absent path: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Text).Value; got != "anonymous" {
		t.Errorf("value = %q, want the default", got)
	}
}

// A path that exists but holds the wrong type is a mismatch, not an absence:
// the default must not paper over it.
func TestGetDefaultDoesNotCoverAWrongType(t *testing.T) {
	r := get(t, "path: count\nas: Text\ndefault: fallback\n", document())

	if !r.Failed() {
		t.Fatal("a present value of the wrong type must fail rather than fall back")
	}
	if r.Err.Code != "type_mismatch" {
		t.Errorf("Code = %q, want %q", r.Err.Code, "type_mismatch")
	}
}

// A default that does not fit the declared type is a configuration error, so it
// fails p6e check rather than the first run that needs it.
func TestGetBadDefaultIsRejectedAtCompileTime(t *testing.T) {
	_, err := GetDefinition().New(withBlock("path: x\nas: Int\ndefault: nope\n"))

	if err == nil {
		t.Fatal("expected an ill-typed default to be rejected when the node is built")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error %q should name the default", err)
	}
}

func TestGetRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		with  string
		wants string
	}{
		{"no path", "as: Text\n", "path"},
		{"empty segment", "path: user..name\nas: Text\n", "empty segment"},
		{"no type", "path: x\n", "as"},
		{"unknown type", "path: x\nas: Duration\n", "unknown type"},
		{"unknown field", "path: x\nas: Text\nfallback: y\n", "fallback"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := GetDefinition().New(withBlock(c.with)); err == nil {
				t.Fatal("expected the configuration to be rejected")
			} else if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error %q should mention %q", err, c.wants)
			}
		})
	}
}

package env

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

func read(t *testing.T, with string) node.ResultValue {
	t.Helper()
	n, err := Definition().New(withBlock(with))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, nil)
}

func TestReadsTheVariableInTheDeclaredType(t *testing.T) {
	t.Setenv("P6E_TEXT", "hello")
	t.Setenv("P6E_INT", "42")
	t.Setenv("P6E_BOOL", "true")

	if got := read(t, "name: P6E_TEXT\nas: Text\n"); got.Failed() {
		t.Errorf("Text: %v", got.Err)
	} else if v := got.Value.Interface().(*types.Text).Value; v != "hello" {
		t.Errorf("Text = %q, want %q", v, "hello")
	}

	if got := read(t, "name: P6E_INT\nas: Int\n"); got.Failed() {
		t.Errorf("Int: %v", got.Err)
	} else if v := got.Value.Interface().(*types.Int).Value; v != 42 {
		t.Errorf("Int = %d, want 42", v)
	}

	if got := read(t, "name: P6E_BOOL\nas: Bool\n"); got.Failed() {
		t.Errorf("Bool: %v", got.Err)
	} else if v := got.Value.Interface().(*types.Bool).Value; !v {
		t.Error("Bool = false, want true")
	}
}

// The value is read at execution, not baked in when the node is built, so one
// compiled plan run twice in different environments sees each one.
func TestValueIsReadAtExecutionNotCompileTime(t *testing.T) {
	t.Setenv("P6E_LATE", "first")
	n, err := Definition().New(withBlock("name: P6E_LATE\nas: Text\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Setenv("P6E_LATE", "second")
	r := n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, nil)

	if got := r.Value.Interface().(*types.Text).Value; got != "second" {
		t.Errorf("value = %q, want %q: the variable must be read when the step runs", got, "second")
	}
}

// p6e check must stay runnable on a machine that does not hold the secrets, so
// building the node cannot require the variable to be set.
func TestBuildsWithoutTheVariableBeingSet(t *testing.T) {
	if _, err := Definition().New(withBlock("name: P6E_DEFINITELY_UNSET\nas: Text\n")); err != nil {
		t.Errorf("building must not require the variable: %v", err)
	}
}

func TestUnsetWithoutADefaultFails(t *testing.T) {
	r := read(t, "name: P6E_DEFINITELY_UNSET\nas: Text\n")

	if !r.Failed() {
		t.Fatal("expected an unset variable with no default to fail")
	}
	if r.Err.Kind != node.KindPermanent {
		t.Errorf("Kind = %q, want %q: the environment is the world, not a pipeline bug", r.Err.Kind, node.KindPermanent)
	}
	if r.Err.Retryable {
		t.Error("no retry will conjure the variable")
	}
}

func TestUnsetWithADefaultUsesIt(t *testing.T) {
	r := read(t, "name: P6E_DEFINITELY_UNSET\nas: Int\ndefault: \"7\"\n")

	if r.Failed() {
		t.Fatalf("a configured default should cover an unset variable: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Int).Value; got != 7 {
		t.Errorf("value = %d, want the default 7", got)
	}
}

// A set variable that cannot parse is a misconfigured environment: reported,
// never silently replaced by the default.
func TestSetButUnparseableFails(t *testing.T) {
	t.Setenv("P6E_BAD", "not-a-number")

	r := read(t, "name: P6E_BAD\nas: Int\ndefault: \"7\"\n")

	if !r.Failed() {
		t.Fatal("expected an unparseable value to fail rather than fall back")
	}
	if r.Err.Code != "bad_value" {
		t.Errorf("Code = %q, want %q", r.Err.Code, "bad_value")
	}
}

// A default that does not fit the declared type is a configuration error, so it
// fails p6e check rather than the first run that needs it.
func TestBadDefaultIsRejectedAtCompileTime(t *testing.T) {
	_, err := Definition().New(withBlock("name: P6E_X\nas: Int\ndefault: \"nope\"\n"))

	if err == nil {
		t.Fatal("expected an unparseable default to be rejected when the node is built")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error %q should name the default", err)
	}
}

func TestOutputTypeFollowsTheDeclaredType(t *testing.T) {
	for as, want := range map[string]node.TypeID{
		"Text": "Text", "Bytes": "Bytes", "Bool": "Bool", "Int": "Int",
	} {
		n, err := Definition().New(withBlock("name: P6E_X\nas: " + as + "\n"))
		if err != nil {
			t.Fatalf("as: %s: %v", as, err)
		}
		d := n.Descriptor()
		if d.Arity() != 0 {
			t.Errorf("as: %s: arity = %d, want 0: env.get is a source", as, d.Arity())
		}
		if d.Output.Type != want {
			t.Errorf("as: %s: output = %q, want %q", as, d.Output.Type, want)
		}
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		with  string
		wants string
	}{
		{"no name", "as: Text\n", "name"},
		{"no type", "name: X\n", "as"},
		{"unknown type", "name: X\nas: Duration\n", "unknown type"},
		{"unknown field", "name: X\nas: Text\nfallback: y\n", "fallback"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Definition().New(withBlock(c.with)); err == nil {
				t.Fatal("expected the configuration to be rejected")
			} else if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error %q should mention %q", err, c.wants)
			}
		})
	}
}

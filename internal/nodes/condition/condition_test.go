package condition

import (
	"context"
	"encoding/json"
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

// document builds the input the way json.decode does, so the test sees the same
// float64 numbers the real chain produces.
func document(t *testing.T, src string) *types.Document {
	t.Helper()
	var root any
	if err := json.Unmarshal([]byte(src), &root); err != nil {
		t.Fatalf("test document is not JSON: %v", err)
	}
	return &types.Document{Root: root}
}

func evaluate(t *testing.T, with, src string) bool {
	t.Helper()
	in := []node.Value{node.NewValue(document(t, src))}
	r := build(t, with).Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
	if r.Failed() {
		t.Fatalf("Execute: %v", r.Err)
	}
	verdict, ok := r.Value.Interface().(*types.Bool)
	if !ok {
		t.Fatalf("output holds %T, want *types.Bool", r.Value.Interface())
	}
	return verdict.Value
}

func TestConditionComparesAValueAtANestedPath(t *testing.T) {
	doc := `{"user":{"name":"ada"}}`

	if !evaluate(t, "path: user.name\nequals: ada\n", doc) {
		t.Error("user.name should equal ada")
	}
	if evaluate(t, "path: user.name\nequals: grace\n", doc) {
		t.Error("user.name should not equal grace")
	}
}

// encoding/json decodes every number as float64, so a configured integer would
// never match a JSON number without normalization.
func TestConditionMatchesNumbersAcrossFloat64AndInt(t *testing.T) {
	if !evaluate(t, "path: count\nequals: 3\n", `{"count":3}`) {
		t.Error("the integer 3 should match the JSON number 3")
	}
	if !evaluate(t, "path: ratio\nequals: 0.5\n", `{"ratio":0.5}`) {
		t.Error("0.5 should match the JSON number 0.5")
	}
	if evaluate(t, "path: count\nequals: 4\n", `{"count":3}`) {
		t.Error("4 should not match the JSON number 3")
	}
}

// Explicit conversions only: the engine coerces nothing, so a string literal
// does not quietly become a number.
func TestConditionDoesNotCoerceAcrossScalarTypes(t *testing.T) {
	if evaluate(t, "path: count\nequals: \"3\"\n", `{"count":3}`) {
		t.Error("the string \"3\" should not match the number 3")
	}
	if evaluate(t, "path: flag\nequals: 1\n", `{"flag":true}`) {
		t.Error("1 should not match true")
	}
}

func TestConditionComparesBooleansAndStrings(t *testing.T) {
	if !evaluate(t, "path: active\nequals: true\n", `{"active":true}`) {
		t.Error("active should equal true")
	}
	if !evaluate(t, "path: role\nequals: \"\"\n", `{"role":""}`) {
		t.Error("an empty string is a value like any other")
	}
}

// A path that is not there is a false verdict, not a failure: the node exists
// to answer questions about documents it does not control.
func TestConditionEqualsOnAMissingPathIsFalseNotAnError(t *testing.T) {
	if evaluate(t, "path: user.email\nequals: ada@example.com\n", `{"user":{"name":"ada"}}`) {
		t.Error("a missing path cannot equal anything")
	}
	if evaluate(t, "path: user.name.first\nequals: ada\n", `{"user":{"name":"ada"}}`) {
		t.Error("descending into a scalar should report no match")
	}
}

func TestConditionExistsReportsPresence(t *testing.T) {
	doc := `{"user":{"name":"ada","email":null}}`

	if !evaluate(t, "path: user.name\nexists: true\n", doc) {
		t.Error("user.name is present")
	}
	if evaluate(t, "path: user.phone\nexists: true\n", doc) {
		t.Error("user.phone is absent")
	}
	// A key set to null is present. Absent and null are different facts, and the
	// node reports the one it was asked about.
	if !evaluate(t, "path: user.email\nexists: true\n", doc) {
		t.Error("a key holding null still exists")
	}
}

// exists: false asserts the absence, which is why exists is a pointer: it has
// to stay distinguishable from an unset field.
func TestConditionExistsFalseAssertsAbsence(t *testing.T) {
	doc := `{"user":{"name":"ada"}}`

	if !evaluate(t, "path: user.phone\nexists: false\n", doc) {
		t.Error("user.phone is absent, so exists: false holds")
	}
	if evaluate(t, "path: user.name\nexists: false\n", doc) {
		t.Error("user.name is present, so exists: false fails")
	}
}

func TestConditionRejectsBothTests(t *testing.T) {
	got := rejected(t, "path: user.name\nequals: ada\nexists: true\n")

	if !strings.Contains(got, "equals") || !strings.Contains(got, "exists") {
		t.Errorf("error %q should name both tests", got)
	}
}

func TestConditionRejectsNoTest(t *testing.T) {
	got := rejected(t, "path: user.name\n")

	if !strings.Contains(got, "equals") || !strings.Contains(got, "exists") {
		t.Errorf("error %q should say which tests are available", got)
	}
}

func TestConditionRejectsAMissingPath(t *testing.T) {
	got := rejected(t, "exists: true\n")

	if !strings.Contains(got, "path") {
		t.Errorf("error %q should say a path is required", got)
	}
}

// "user..name" and "user." are typos, and a path with an empty segment can
// never match anything, so failing at check time is the only useful answer.
func TestConditionRejectsAnEmptyPathSegment(t *testing.T) {
	for _, path := range []string{"user..name", "user.", ".user"} {
		got := rejected(t, "path: "+path+"\nexists: true\n")
		if !strings.Contains(got, path) {
			t.Errorf("error %q should quote the rejected path %q", got, path)
		}
	}
}

// Comparing two maps with == panics at run time, so a non-scalar equals is
// refused while there is still a compiler to refuse it.
func TestConditionRejectsANonScalarEquals(t *testing.T) {
	got := rejected(t, "path: user\nequals:\n  name: ada\n")

	if !strings.Contains(got, "scalar") {
		t.Errorf("error %q should explain that equals must be a scalar", got)
	}
}

func TestConditionRejectsAnUnknownConfigField(t *testing.T) {
	got := rejected(t, "path: user.name\nequals: ada\nmatches: ada\n")

	if !strings.Contains(got, "matches") {
		t.Errorf("error %q should name the unknown field", got)
	}
}

func TestConditionConfigErrorsAreInvalidInput(t *testing.T) {
	_, err := Definition().New(withBlock("exists: true\n"))

	var ne *node.NodeError
	if !errors.As(err, &ne) {
		t.Fatalf("err is %T, want *node.NodeError", err)
	}
	if ne.Kind != node.KindInvalidInput {
		t.Errorf("Kind = %q, want %q", ne.Kind, node.KindInvalidInput)
	}
	if ne.Retryable {
		t.Error("a configuration error must not be retryable")
	}
}

func TestConditionDeclaresDocumentToBool(t *testing.T) {
	d := build(t, "path: user.name\nexists: true\n").Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "JSONDocument" {
		t.Errorf("inputs = %s, want (JSONDocument)", d.InputTypes())
	}
	if d.Output.Type != "Bool" {
		t.Errorf("output type = %q, want %q", d.Output.Type, "Bool")
	}
}

package httpnode

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func run(t *testing.T, def node.Definition, cfg node.Config, resp *types.Response) node.ResultValue {
	t.Helper()
	n, err := def.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := []node.Value{node.NewValue(resp)}
	return n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
}

// A non-2xx status is data. This is the node that makes that usable, so a 404
// must arrive as a value rather than a failure.
func TestStatusReportsANonSuccessCodeAsAValue(t *testing.T) {
	r := run(t, StatusDefinition(), node.EmptyConfig, &types.Response{Status: 404})

	if r.Failed() {
		t.Fatalf("a 404 must arrive as data, not an error: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Int).Value; got != 404 {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestStatusDeclaresResponseToInt(t *testing.T) {
	n, err := StatusDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := n.Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "HTTPResponse" {
		t.Errorf("inputs = %s, want (HTTPResponse)", d.InputTypes())
	}
	if d.Output.Type != "Int" {
		t.Errorf("output type = %q, want %q", d.Output.Type, "Int")
	}
}

func response(pairs ...string) *types.Response {
	h := http.Header{}
	for i := 0; i < len(pairs); i += 2 {
		h.Add(pairs[i], pairs[i+1])
	}
	return &types.Response{Status: 200, Headers: h}
}

// HTTP header names are case insensitive, and a pipeline should not have to
// guess how the server capitalized them.
func TestHeaderIsCaseInsensitive(t *testing.T) {
	r := run(t, HeaderDefinition(), yamlConfig("name: content-type\n"),
		response("Content-Type", "application/json"))

	if r.Failed() {
		t.Fatalf("http.header: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Text).Value; got != "application/json" {
		t.Errorf("header = %q, want %q", got, "application/json")
	}
}

// Producing "" for a missing header would be indistinguishable from a header
// that is genuinely empty, and that value would flow on unnoticed.
func TestHeaderAbsentWithoutADefaultFails(t *testing.T) {
	r := run(t, HeaderDefinition(), yamlConfig("name: Retry-After\n"), response())

	if !r.Failed() {
		t.Fatal("expected an absent header with no default to fail")
	}
	if r.Err.Kind != node.KindPermanent {
		t.Errorf("Kind = %q, want %q", r.Err.Kind, node.KindPermanent)
	}
	if r.Err.Code != "header_absent" {
		t.Errorf("Code = %q, want %q", r.Err.Code, "header_absent")
	}
	if r.Err.Retryable {
		t.Error("the same response will not grow the header on a retry")
	}
}

func TestHeaderAbsentWithADefaultUsesIt(t *testing.T) {
	r := run(t, HeaderDefinition(), yamlConfig("name: Retry-After\ndefault: \"0\"\n"), response())

	if r.Failed() {
		t.Fatalf("a configured default should cover an absent header: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Text).Value; got != "0" {
		t.Errorf("header = %q, want the default %q", got, "0")
	}
}

// An empty default is a real choice, distinct from declaring none, so it must
// not be mistaken for an absent one.
func TestHeaderEmptyDefaultIsHonoured(t *testing.T) {
	r := run(t, HeaderDefinition(), yamlConfig("name: X-Trace\ndefault: \"\"\n"), response())

	if r.Failed() {
		t.Fatalf("an empty default is still a default: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Text).Value; got != "" {
		t.Errorf("header = %q, want the empty default", got)
	}
}

// A header present but empty is a value, not an absence, so the default must
// not displace it.
func TestHeaderPresentButEmptyIsNotAbsent(t *testing.T) {
	r := run(t, HeaderDefinition(), yamlConfig("name: X-Trace\ndefault: fallback\n"),
		response("X-Trace", ""))

	if r.Failed() {
		t.Fatalf("http.header: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Text).Value; got != "" {
		t.Errorf("header = %q, want the empty value the server sent", got)
	}
}

func TestHeaderRepeatedTakesTheFirst(t *testing.T) {
	r := run(t, HeaderDefinition(), yamlConfig("name: Set-Cookie\n"),
		response("Set-Cookie", "a=1", "Set-Cookie", "b=2"))

	if r.Failed() {
		t.Fatalf("http.header: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Text).Value; got != "a=1" {
		t.Errorf("header = %q, want the first value", got)
	}
}

func TestHeaderRequiresAName(t *testing.T) {
	_, err := HeaderDefinition().New(node.EmptyConfig)

	if err == nil {
		t.Fatal("expected a missing name to be rejected at compile time")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q should say a name is required", err)
	}
}

func TestHeaderRejectsAnUnknownField(t *testing.T) {
	_, err := HeaderDefinition().New(yamlConfig("name: Accept\nfallback: x\n"))

	if err == nil {
		t.Fatal("expected an unknown field to be rejected")
	}
	if !strings.Contains(err.Error(), "fallback") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

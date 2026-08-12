package httpnode

import (
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func assertStatus(t *testing.T, with string, status int) node.ResultValue {
	t.Helper()
	return run(t, AssertStatusDefinition(), yamlConfig(with), &types.Response{Status: status})
}

func TestAssertStatusAcceptsAnExactCode(t *testing.T) {
	if r := assertStatus(t, "equals: 200\n", 200); r.Failed() {
		t.Errorf("200 should satisfy equals: 200: %v", r.Err)
	}
	if r := assertStatus(t, "equals: 200\n", 201); !r.Failed() {
		t.Error("201 should not satisfy equals: 200")
	}
}

func TestAssertStatusAcceptsARange(t *testing.T) {
	for _, status := range []int{200, 204, 299} {
		if r := assertStatus(t, "min: 200\nmax: 299\n", status); r.Failed() {
			t.Errorf("%d should satisfy 200 to 299: %v", status, r.Err)
		}
	}
	for _, status := range []int{199, 300, 404, 500} {
		if r := assertStatus(t, "min: 200\nmax: 299\n", status); !r.Failed() {
			t.Errorf("%d should not satisfy 200 to 299", status)
		}
	}
}

// A range open at one end is useful: "anything but a server error" is a min or
// a max, not a pair.
func TestAssertStatusAcceptsAnOpenRange(t *testing.T) {
	if r := assertStatus(t, "max: 499\n", 404); r.Failed() {
		t.Errorf("404 should satisfy max: 499: %v", r.Err)
	}
	if r := assertStatus(t, "max: 499\n", 500); !r.Failed() {
		t.Error("500 should not satisfy max: 499")
	}
	if r := assertStatus(t, "min: 400\n", 503); r.Failed() {
		t.Errorf("503 should satisfy min: 400: %v", r.Err)
	}
}

// The response passes through, so the steps that read it can depend on this one
// and will not run at all when the status is wrong.
func TestAssertStatusPassesTheResponseThrough(t *testing.T) {
	resp := &types.Response{Status: 200, Body: []byte("payload")}

	r := run(t, AssertStatusDefinition(), yamlConfig("equals: 200\n"), resp)

	if r.Failed() {
		t.Fatalf("http.assert_status: %v", r.Err)
	}
	if r.Value.Interface().(*types.Response) != resp {
		t.Error("the response should pass through by reference, not be rebuilt")
	}
}

func TestAssertStatusFailureNamesBothCodes(t *testing.T) {
	r := assertStatus(t, "min: 200\nmax: 299\n", 503)

	if !r.Failed() {
		t.Fatal("expected 503 to fail")
	}
	if r.Err.Kind != node.KindPermanent {
		t.Errorf("Kind = %q, want %q", r.Err.Kind, node.KindPermanent)
	}
	if r.Err.Code != "unexpected_status" {
		t.Errorf("Code = %q, want %q", r.Err.Code, "unexpected_status")
	}
	if r.Err.Retryable {
		t.Error("re-testing the same response reaches the same answer")
	}
	if !strings.Contains(r.Err.Message, "503") || !strings.Contains(r.Err.Message, "200 to 299") {
		t.Errorf("Message = %q, want it to name what arrived and what was wanted", r.Err.Message)
	}
}

func TestAssertStatusDeclaresResponseToResponse(t *testing.T) {
	n, err := AssertStatusDefinition().New(yamlConfig("equals: 200\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := n.Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "HTTPResponse" {
		t.Errorf("inputs = %s, want (HTTPResponse)", d.InputTypes())
	}
	if d.Output.Type != "HTTPResponse" {
		t.Errorf("output type = %q, want %q", d.Output.Type, "HTTPResponse")
	}
}

func TestAssertStatusRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		with  string
		wants string
	}{
		{"no test", "", "requires equals"},
		{"both forms", "equals: 200\nmin: 200\n", "not both"},
		{"inverted range", "min: 300\nmax: 200\n", "accepts nothing"},
		{"not a status", "equals: 20\n", "not an HTTP status code"},
		{"status too high", "max: 600\n", "not an HTTP status code"},
		{"unknown field", "equals: 200\nretry: true\n", "retry"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := AssertStatusDefinition().New(yamlConfig(c.with))
			if err == nil {
				t.Fatal("expected the configuration to be rejected")
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error %q should mention %q", err, c.wants)
			}
		})
	}
}

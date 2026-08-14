package httpnode

import (
	"context"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func compose(t *testing.T, def node.Definition, cfg node.Config, inputs ...node.Value) node.ResultValue {
	t.Helper()
	n, err := def.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, inputs)
}

func text(s string) node.Value { return node.NewValue(&types.Text{Value: s}) }

func TestFromURLBuildsARequestFromAnEdge(t *testing.T) {
	r := compose(t, FromURLDefinition(), yamlConfig("method: post\n"),
		text("https://example.com/api"))

	if r.Failed() {
		t.Fatalf("http.from_url: %v", r.Err)
	}
	req := r.Value.Interface().(*types.Request)
	if req.URL.String() != "https://example.com/api" {
		t.Errorf("URL = %q, want the one that arrived", req.URL)
	}
	if req.Method != "POST" {
		t.Errorf("Method = %q, want POST: the configured method is upper-cased", req.Method)
	}
}

func TestFromURLDefaultsToGET(t *testing.T) {
	r := compose(t, FromURLDefinition(), node.EmptyConfig, text("https://example.com"))

	if got := r.Value.Interface().(*types.Request).Method; got != "GET" {
		t.Errorf("Method = %q, want GET", got)
	}
}

// A URL arriving on an edge cannot be checked until it arrives, so the check
// moves to execution rather than disappearing.
func TestFromURLRejectsABadURLAtExecution(t *testing.T) {
	for _, bad := range []string{"", "ftp://example.com", "/relative/only", "not a url"} {
		r := compose(t, FromURLDefinition(), node.EmptyConfig, text(bad))
		if !r.Failed() {
			t.Errorf("expected %q to be rejected", bad)
			continue
		}
		if r.Err.Kind != node.KindInvalidInput {
			t.Errorf("%q: Kind = %q, want %q", bad, r.Err.Kind, node.KindInvalidInput)
		}
	}
}

func TestWithHeaderSetsAndReplaces(t *testing.T) {
	base := &types.Request{URL: checked(t, "https://example.com"), Headers: map[string]string{"Accept": "text/plain"}}

	r := compose(t, WithHeaderDefinition(), yamlConfig("name: Authorization\n"),
		node.NewValue(base), text("Bearer t"))

	if r.Failed() {
		t.Fatalf("http.with_header: %v", r.Err)
	}
	got := r.Value.Interface().(*types.Request)
	if got.Headers["Authorization"] != "Bearer t" {
		t.Errorf("Authorization = %q, want the value that arrived", got.Headers["Authorization"])
	}
	if got.Headers["Accept"] != "text/plain" {
		t.Error("existing headers should survive")
	}
}

// Header names are canonicalised so setting "content-type" over "Content-Type"
// replaces it rather than leaving the request carrying both.
func TestWithHeaderCanonicalisesNames(t *testing.T) {
	base := &types.Request{Headers: map[string]string{"Content-Type": "text/plain"}}

	r := compose(t, WithHeaderDefinition(), yamlConfig("name: content-type\n"),
		node.NewValue(base), text("application/json"))

	got := r.Value.Interface().(*types.Request).Headers
	if len(got) != 1 {
		t.Errorf("headers = %v, want one entry: the name should have been replaced", got)
	}
	if got["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want the new value", got["Content-Type"])
	}
}

// Values on edges are immutable. The request this node receives may fan out to
// a sibling step, and a retry hands the same input to the next attempt.
func TestComposeDoesNotMutateItsInput(t *testing.T) {
	base := &types.Request{
		URL:     checked(t, "https://example.com"),
		Headers: map[string]string{"Accept": "text/plain"},
		Body:    []byte("original"),
	}

	compose(t, WithHeaderDefinition(), yamlConfig("name: Accept\n"),
		node.NewValue(base), text("application/json"))
	compose(t, WithBodyDefinition(), node.EmptyConfig,
		node.NewValue(base), node.NewValue(&types.Bytes{Value: []byte("replaced")}))

	if base.Headers["Accept"] != "text/plain" {
		t.Errorf("the input's headers were mutated: Accept = %q", base.Headers["Accept"])
	}
	if string(base.Body) != "original" {
		t.Errorf("the input's body was mutated: %q", base.Body)
	}
	if len(base.Headers) != 1 {
		t.Errorf("the input's header map grew: %v", base.Headers)
	}
}

func TestWithBodySetsTheBody(t *testing.T) {
	base := &types.Request{URL: checked(t, "https://example.com"), Method: "POST"}

	r := compose(t, WithBodyDefinition(), node.EmptyConfig,
		node.NewValue(base), node.NewValue(&types.Bytes{Value: []byte(`{"a":1}`)}))

	if r.Failed() {
		t.Fatalf("http.with_body: %v", r.Err)
	}
	got := r.Value.Interface().(*types.Request)
	if string(got.Body) != `{"a":1}` {
		t.Errorf("Body = %q, want the bytes that arrived", got.Body)
	}
	if got.Method != "POST" || got.URL != base.URL {
		t.Error("everything other than the body should carry over")
	}
}

func TestComposeDeclaresSignatures(t *testing.T) {
	from, err := FromURLDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := from.Descriptor()
	if d.Arity() != 1 || d.Inputs[0].Type != "Text" || d.Output.Type != "HTTPRequest" {
		t.Errorf("http.from_url is %s -> %s, want (Text) -> HTTPRequest", d.InputTypes(), d.Output.Type)
	}

	header, err := WithHeaderDefinition().New(yamlConfig("name: Accept\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d = header.Descriptor()
	if d.Arity() != 2 || d.Inputs[0].Type != "HTTPRequest" || d.Inputs[1].Type != "Text" {
		t.Errorf("http.with_header is %s, want (HTTPRequest, Text)", d.InputTypes())
	}

	body, err := WithBodyDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d = body.Descriptor()
	if d.Arity() != 2 || d.Inputs[0].Type != "HTTPRequest" || d.Inputs[1].Type != "Bytes" {
		t.Errorf("http.with_body is %s, want (HTTPRequest, Bytes)", d.InputTypes())
	}
}

func TestComposeRejectsInvalidConfiguration(t *testing.T) {
	if _, err := WithHeaderDefinition().New(node.EmptyConfig); err == nil {
		t.Error("http.with_header requires a name")
	}
	if _, err := WithBodyDefinition().New(yamlConfig("name: x\n")); err == nil {
		t.Error("http.with_body takes no configuration")
	}
	if _, err := FromURLDefinition().New(yamlConfig("url: https://x.com\n")); err == nil {
		t.Error("http.from_url takes its url from an edge, not a with block")
	} else if !strings.Contains(err.Error(), "url") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

package httpnode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func buildRequest(t *testing.T, with string) (*types.Request, error) {
	t.Helper()

	n, err := BuildDefinition().New(yamlConfig(with))
	if err != nil {
		return nil, err
	}
	result := n.Execute(context.Background(), testEC(), nil)
	if result.Failed() {
		t.Fatalf("Execute: %v", result.Err)
	}
	return result.Value.Interface().(*types.Request), nil
}

func TestBuildDefinitionBuildsFromConfig(t *testing.T) {
	req, err := buildRequest(t, "method: post\nurl: https://example.com/api\nheaders:\n  X-Token: secret\nbody: payload\n")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if req.Method != "POST" {
		t.Errorf("Method = %q, want POST: methods are normalized to upper case", req.Method)
	}
	if req.URL != "https://example.com/api" {
		t.Errorf("URL = %q, want https://example.com/api", req.URL)
	}
	if req.Headers["X-Token"] != "secret" {
		t.Errorf("Headers = %v, want X-Token preserved", req.Headers)
	}
	if string(req.Body) != "payload" {
		t.Errorf("Body = %q, want payload", req.Body)
	}
}

func TestBuildDefaultsToGET(t *testing.T) {
	req, err := buildRequest(t, "url: https://example.com\n")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if req.Method != "GET" {
		t.Errorf("Method = %q, want GET", req.Method)
	}
}

// A typo in a URL should fail p6e check, not a production run.
func TestBuildRejectsUnusableURLs(t *testing.T) {
	cases := map[string]string{
		"missing":         "method: GET\n",
		"empty":           "url: \"\"\n",
		"no scheme":       "url: example.com/api\n",
		"wrong scheme":    "url: ftp://example.com\n",
		"no host":         "url: https://\n",
		"unparseable":     "url: \"http://exa mple.com\"\n",
		"scheme only":     "url: https:\n",
		"relative path":   "url: /api/users\n",
		"trailing spaces": "url: \"   \"\n",
	}
	for name, with := range cases {
		if _, err := BuildDefinition().New(yamlConfig(with)); err == nil {
			t.Errorf("%s: expected a configuration error", name)
		}
	}
}

func TestBuildRejectsUnknownField(t *testing.T) {
	if _, err := BuildDefinition().New(yamlConfig("url: https://example.com\nheder: x\n")); err == nil {
		t.Error("a typo in the with block should fail at check time")
	}
}

func TestBuildProducesRequestType(t *testing.T) {
	n, err := BuildDefinition().New(yamlConfig("url: https://example.com\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	desc := n.Descriptor()
	if desc.Arity() != 0 {
		t.Errorf("Arity = %d, want 0: this is a source", desc.Arity())
	}
	if desc.Output.Type != "HTTPRequest" {
		t.Errorf("output type = %q, want HTTPRequest", desc.Output.Type)
	}
}

// The three http nodes have to chain: build produces what request consumes, and
// request produces what body consumes. If these types drift apart, no pipeline
// can use them together.
func TestHTTPNodesChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "secret" {
			t.Errorf("header did not reach the server, got %q", r.Header.Get("X-Token"))
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	build, err := BuildDefinition().New(yamlConfig("url: " + server.URL + "\nheaders:\n  X-Token: secret\n"))
	if err != nil {
		t.Fatalf("build New: %v", err)
	}
	request, err := RequestDefinition().New(yamlConfig(allowPrivate))
	if err != nil {
		t.Fatalf("request New: %v", err)
	}
	body, err := BodyDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("body New: %v", err)
	}

	ctx := context.Background()
	built := build.Execute(ctx, testEC(), nil)
	sent := request.Execute(ctx, testEC(), []node.Value{built.Value})
	if sent.Failed() {
		t.Fatalf("http.request failed: %v", sent.Err)
	}
	extracted := body.Execute(ctx, testEC(), []node.Value{sent.Value})
	if extracted.Failed() {
		t.Fatalf("http.body failed: %v", extracted.Err)
	}

	if got := string(extracted.Value.Interface().(*types.Bytes).Value); got != `{"ok":true}` {
		t.Errorf("body = %q, want the server's response", got)
	}
}

func TestBuiltRequestIsSharedAcrossExecutions(t *testing.T) {
	n, err := BuildDefinition().New(yamlConfig("url: https://example.com\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := n.Execute(context.Background(), testEC(), nil)
	second := n.Execute(context.Background(), testEC(), nil)

	if first.Value.Interface().(*types.Request) != second.Value.Interface().(*types.Request) {
		t.Error("two executions received different Request pointers")
	}
}

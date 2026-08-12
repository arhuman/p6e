package httpnode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"gopkg.in/yaml.v3"
)

// yamlConfig is a with block, decoded exactly as the compiler decodes one:
// strictly, so a test can check that an unknown field is rejected.
type yamlConfig string

func (c yamlConfig) Decode(dst any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(c)))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func testEC() *node.ExecutionContext {
	return &node.ExecutionContext{WorkflowID: "w", ExecutionID: "e", StepID: "s", Attempt: 1}
}

// newRequestNode builds the node the way the compiler does, so the tests
// exercise the registered definition rather than reaching past it.
func newRequestNode(t *testing.T, with string) node.RuntimeNode {
	t.Helper()
	n, err := RequestDefinition().New(yamlConfig(with))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n
}

func request(t *testing.T, ctx context.Context, with string, req *types.Request) node.ResultValue {
	t.Helper()
	return newRequestNode(t, with).Execute(ctx, testEC(), []node.Value{node.NewValue(req)})
}

func succeeds(t *testing.T, r node.ResultValue) *types.Response {
	t.Helper()
	if r.Failed() {
		t.Fatalf("execution failed: %v", r.Err)
	}
	resp, ok := r.Value.Interface().(*types.Response)
	if !ok {
		t.Fatalf("result holds %T, want *types.Response", r.Value.Interface())
	}
	return resp
}

func fails(t *testing.T, r node.ResultValue) *node.NodeError {
	t.Helper()
	if !r.Failed() {
		t.Fatalf("expected a failure, got %+v", r.Value.Interface())
	}
	return r.Err
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// hang serves a request that never answers on its own. Cleanup releases the
// handler before the server closes, so a test cannot deadlock on shutdown.
func hang(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func TestReturnsStatusHeadersAndBody(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Origin", "test")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "hello")
	})

	resp := succeeds(t, request(t, t.Context(), "", &types.Request{URL: srv.URL}))

	if resp.Status != http.StatusCreated {
		t.Errorf("Status = %d, want %d", resp.Status, http.StatusCreated)
	}
	if got := string(resp.Body); got != "hello" {
		t.Errorf("Body = %q, want %q", got, "hello")
	}
	if got := resp.Headers.Get("X-Origin"); got != "test" {
		t.Errorf("X-Origin = %q, want %q", got, "test")
	}
}

// The point of the node: whether a 404 ends the pipeline is the workflow's
// call. Reporting it as a node error would let retry policy act on an answer.
func TestNon2xxStatusIsDataNotAnError(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "no such thing")
	})

	resp := succeeds(t, request(t, t.Context(), "", &types.Request{URL: srv.URL}))

	if resp.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want 404", resp.Status)
	}
	if got := string(resp.Body); got != "no such thing" {
		t.Errorf("Body = %q, want the error document the server sent", got)
	}
}

func TestSendsMethodHeadersAndBody(t *testing.T) {
	var gotMethod, gotHeader, gotBody string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotHeader, gotBody = r.Method, r.Header.Get("X-Token"), string(body)
	})

	succeeds(t, request(t, t.Context(), "", &types.Request{
		Method:  http.MethodPost,
		URL:     srv.URL,
		Headers: map[string]string{"X-Token": "secret"},
		Body:    []byte(`{"a":1}`),
	}))

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotHeader != "secret" {
		t.Errorf("X-Token = %q, want %q", gotHeader, "secret")
	}
	if gotBody != `{"a":1}` {
		t.Errorf("body = %q, want the request payload", gotBody)
	}
}

// A refused connection is the world being briefly unavailable, which is exactly
// what a retry policy is for.
func TestConnectionRefusedIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	err := fails(t, request(t, t.Context(), "", &types.Request{URL: url}))

	if err.Kind != node.KindTransient {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindTransient)
	}
	if !err.Retryable {
		t.Error("a refused connection should be retryable")
	}
}

func TestTimeoutIsTransient(t *testing.T) {
	srv := hang(t)

	err := fails(t, request(t, t.Context(), "timeout: 50ms\n", &types.Request{URL: srv.URL}))

	if err.Kind != node.KindTransient {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindTransient)
	}
	if err.Code != "timeout" {
		t.Errorf("Code = %q, want %q", err.Code, "timeout")
	}
}

// A client timeout also reports DeadlineExceeded, so the node has to consult
// the context to tell an abandoned execution from an overrun call.
func TestCancelledContextIsCancelled(t *testing.T) {
	srv := hang(t)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := fails(t, request(t, ctx, "timeout: 30s\n", &types.Request{URL: srv.URL}))

	if err.Kind != node.KindCancelled {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindCancelled)
	}
	if err.Retryable {
		t.Error("a cancelled execution must not be retryable")
	}
}

// Truncating would hand the next step a body that looks complete and is not.
// The limit is a refusal, not a silent trim.
func TestBodyOverTheLimitFailsRatherThanTruncates(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, 2048))
	})

	err := fails(t, request(t, t.Context(), "max_body_bytes: 1024\n", &types.Request{URL: srv.URL}))

	if err.Kind != node.KindPermanent {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindPermanent)
	}
	if err.Code != "body_too_large" {
		t.Errorf("Code = %q, want %q", err.Code, "body_too_large")
	}
}

// The limit is a maximum, not a threshold one byte below: a body of exactly
// max_body_bytes is legal and must read whole.
func TestBodyExactlyAtTheLimitIsKept(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, 1024))
	})

	resp := succeeds(t, request(t, t.Context(), "max_body_bytes: 1024\n", &types.Request{URL: srv.URL}))

	if len(resp.Body) != 1024 {
		t.Errorf("read %d bytes, want the whole 1024 byte body", len(resp.Body))
	}
}

// Configuration is checked once, at compile time, so a pipeline that cannot
// work never runs at all.
func TestRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name string
		with string
	}{
		{"timeout is not a duration", "timeout: soon\n"},
		{"timeout is a bare number", "timeout: 10\n"},
		{"timeout is negative", "timeout: -1s\n"},
		{"timeout is zero", "timeout: 0s\n"},
		{"max_body_bytes is negative", "max_body_bytes: -1\n"},
		{"unknown field is a typo", "timeuot: 10s\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := RequestDefinition().New(yamlConfig(c.with)); err == nil {
				t.Errorf("New accepted %q", c.with)
			}
		})
	}
}

func TestAppliesDefaultsWhenUnconfigured(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, 64<<10))
	})

	resp := succeeds(t, request(t, t.Context(), "", &types.Request{URL: srv.URL}))

	if len(resp.Body) != 64<<10 {
		t.Errorf("read %d bytes, want the whole body under the default limit", len(resp.Body))
	}
}

// No retry fixes a URL the graph cannot express or a method the protocol does
// not have, so these must not be reported as transient.
func TestUnusableRequestsArePermanent(t *testing.T) {
	cases := []struct {
		name string
		req  *types.Request
		code string
	}{
		{"malformed URL", &types.Request{URL: "http://%zz"}, "invalid_request"},
		{"invalid method token", &types.Request{Method: "GE T", URL: "http://example.invalid"}, "invalid_request"},
		{"unsupported scheme", &types.Request{URL: "ftp://example.invalid/f"}, "unsupported_scheme"},
		{"no scheme at all", &types.Request{URL: "example.invalid/f"}, "unsupported_scheme"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := fails(t, request(t, t.Context(), "", c.req))

			if err.Kind != node.KindPermanent {
				t.Errorf("Kind = %q, want %q", err.Kind, node.KindPermanent)
			}
			if err.Code != c.code {
				t.Errorf("Code = %q, want %q", err.Code, c.code)
			}
		})
	}
}

func TestRequestDescriptorTypesTheEdges(t *testing.T) {
	d := newRequestNode(t, "").Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "HTTPRequest" {
		t.Errorf("inputs = %s, want (HTTPRequest)", d.InputTypes())
	}
	if d.Output.Type != "HTTPResponse" {
		t.Errorf("output = %q, want %q", d.Output.Type, "HTTPResponse")
	}
}

func newBodyNode(t *testing.T) node.RuntimeNode {
	t.Helper()
	n, err := BodyDefinition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n
}

// The extractor exists so a pipeline can state the conversion the engine
// refuses to perform for it, and it must stay free: sharing the backing array
// is safe because values are immutable.
func TestBodyExtractsWithoutCopying(t *testing.T) {
	resp := &types.Response{Status: 200, Body: []byte("payload")}

	r := newBodyNode(t).Execute(t.Context(), testEC(), []node.Value{node.NewValue(resp)})

	if r.Failed() {
		t.Fatalf("execution failed: %v", r.Err)
	}
	got, ok := r.Value.Interface().(*types.Bytes)
	if !ok {
		t.Fatalf("result holds %T, want *types.Bytes", r.Value.Interface())
	}
	if &got.Value[0] != &resp.Body[0] {
		t.Error("the body was copied instead of shared")
	}
}

func TestBodyDescriptorTypesTheEdges(t *testing.T) {
	d := newBodyNode(t).Descriptor()

	if d.Arity() != 1 || d.Inputs[0].Type != "HTTPResponse" {
		t.Errorf("inputs = %s, want (HTTPResponse)", d.InputTypes())
	}
	if d.Output.Type != "Bytes" {
		t.Errorf("output = %q, want %q", d.Output.Type, "Bytes")
	}
}

func TestBodyDefinitionRejectsAWithBlock(t *testing.T) {
	if _, err := BodyDefinition().New(yamlConfig("limit: 10\n")); err == nil {
		t.Fatal("expected a with block to be rejected")
	}
}

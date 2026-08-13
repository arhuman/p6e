package trigger

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func TestWebhookDescriptor(t *testing.T) {
	trg := build(t, WebhookName, "path: /hooks/deploy")

	for name, want := range map[string]node.TypeID{
		"body": "Bytes", "method": "Text", "path": "Text", "query": "Text",
	} {
		got, ok := trg.Descriptor().Provided(name)
		if !ok {
			t.Errorf("webhook does not provide %q", name)
			continue
		}
		if got != want {
			t.Errorf("webhook provides %q as %s, want %s", name, got, want)
		}
	}
}

// The claim is what the daemon compares across pipelines, so it has to
// distinguish two routes that differ only by method.
func TestWebhookClaim(t *testing.T) {
	post := build(t, WebhookName, "path: /hooks/deploy")
	get := build(t, WebhookName, "{path: /hooks/deploy, method: get}")

	if got := post.Claim(); got.Kind != "http" || got.Key != "POST /hooks/deploy" {
		t.Errorf("Claim() = %v, want http POST /hooks/deploy", got)
	}
	if post.Claim() == get.Claim() {
		t.Error("two methods on one path should be distinct claims")
	}
}

func TestWebhookDefaultsToPost(t *testing.T) {
	trg := build(t, WebhookName, "path: /hooks/deploy")

	if !strings.HasPrefix(trg.Claim().Key, http.MethodPost) {
		t.Errorf("Claim() = %v, want a POST route by default", trg.Claim())
	}
}

func TestWebhookLowercaseMethodIsAccepted(t *testing.T) {
	trg := build(t, WebhookName, "{path: /x, method: put}")

	if got := trg.Claim().Key; got != "PUT /x" {
		t.Errorf("Claim key = %q, want %q", got, "PUT /x")
	}
}

func TestWebhookRejectsBadConfig(t *testing.T) {
	for name, tc := range map[string]struct{ cfg, want config }{
		"no path":        {"method: post", "path"},
		"relative path":  {"path: hooks/deploy", `must start with "/"`},
		"unknown method": {"{path: /x, method: FETCH}", "unknown method"},
		"negative body":  {"{path: /x, max_body: -1}", "negative"},
		"unknown field":  {"{path: /x, pathh: /y}", "pathh"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := buildErr(t, WebhookName, tc.cfg); !strings.Contains(got, string(tc.want)) {
				t.Errorf("error %q should mention %q", got, string(tc.want))
			}
		})
	}
}

func TestWebhookValues(t *testing.T) {
	trg := build(t, WebhookName, "path: /hooks/deploy").(HTTPDriven)

	r := httptest.NewRequest(http.MethodPost, "/hooks/deploy?ref=main", strings.NewReader(`{"ok":true}`))
	values, err := trg.Values(r)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}

	body, ok := values["body"].Interface().(*types.Bytes)
	if !ok {
		t.Fatalf("body is %T, want *types.Bytes", values["body"].Interface())
	}
	if string(body.Value) != `{"ok":true}` {
		t.Errorf("body = %q, want the request body", body.Value)
	}
	if got := values["query"].Interface().(*types.Text).Value; got != "ref=main" {
		t.Errorf("query = %q, want %q", got, "ref=main")
	}
	if got := values["path"].Interface().(*types.Text).Value; got != "/hooks/deploy" {
		t.Errorf("path = %q, want %q", got, "/hooks/deploy")
	}
	if got := values["method"].Interface().(*types.Text).Value; got != http.MethodPost {
		t.Errorf("method = %q, want %q", got, http.MethodPost)
	}
}

// Every provided value carries the type the descriptor promised, because the
// compiler type checked the pipeline against that promise and never sees these.
func TestWebhookValuesMatchDescriptor(t *testing.T) {
	trg := build(t, WebhookName, "path: /x").(HTTPDriven)

	values, err := trg.Values(httptest.NewRequest(http.MethodPost, "/x", nil))
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	for _, port := range trg.Descriptor().Provides {
		value, ok := values[port.Name]
		if !ok {
			t.Errorf("descriptor promises %q but Values omits it", port.Name)
			continue
		}
		if value.Type() != port.Type {
			t.Errorf("%q is supplied as %s but declared %s", port.Name, value.Type(), port.Type)
		}
	}
}

// An oversized body is refused before a run starts: the whole point of reading
// it in the trigger is that nothing downstream has been committed to yet.
func TestWebhookRejectsOversizedBody(t *testing.T) {
	trg := build(t, WebhookName, "{path: /x, max_body: 8}").(HTTPDriven)

	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(strings.Repeat("a", 9)))
	if _, err := trg.Values(r); err == nil {
		t.Fatal("a body over max_body should be rejected")
	}

	r = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(strings.Repeat("a", 8)))
	if _, err := trg.Values(r); err != nil {
		t.Errorf("a body exactly at max_body should be accepted, got %v", err)
	}
}

package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
)

const (
	testSecretEnv = "P6E_TEST_WEBHOOK_SECRET"
	testSecret    = "s3cret"
	authWith      = "path: /hooks/deploy\nauth:\n  scheme: hmac-sha256\n  header: X-Hub-Signature-256\n  prefix: \"sha256=\"\n  secret_env: " + testSecretEnv + "\n"
)

// signed builds the header value a correctly-signing sender would send.
func signed(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(body string, header, value string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/hooks/deploy", strings.NewReader(body))
	if header != "" {
		r.Header.Set(header, value)
	}
	return r
}

// Everything checkable without the secret is checked at compile time, so a
// malformed auth block fails p6e check rather than the first real event.
func TestWebhookAuthConfigIsValidatedAtCompileTime(t *testing.T) {
	for name, cfg := range map[string]string{
		"unknown scheme": "path: /h\nauth:\n  scheme: rot13\n  header: X-Sig\n  secret_env: S\n",
		"no header":      "path: /h\nauth:\n  scheme: hmac-sha256\n  secret_env: S\n",
		"no secret_env":  "path: /h\nauth:\n  scheme: hmac-sha256\n  header: X-Sig\n",
	} {
		t.Run(name, func(t *testing.T) {
			if msg := buildErr(t, WebhookName, config(cfg)); msg == "" {
				t.Error("expected the configuration to be rejected")
			}
		})
	}
}

// Compiling must not read the secret: a pipeline whose secret exists only in
// production still has to validate anywhere, which is the same bargain env.get
// makes and what keeps `p6e check` free of credentials.
func TestWebhookAuthCompilesWithoutTheSecretPresent(t *testing.T) {
	t.Setenv(testSecretEnv, "")
	build(t, WebhookName, config(authWith))
}

func TestWebhookAcceptsACorrectlySignedEvent(t *testing.T) {
	t.Setenv(testSecretEnv, testSecret)
	w := build(t, WebhookName, config(authWith)).(*webhook)

	body := `{"ref":"refs/heads/main"}`
	values, err := w.Values(post(body, "X-Hub-Signature-256", signed(testSecret, body)))
	if err != nil {
		t.Fatalf("a correctly signed event was rejected: %v", err)
	}
	if got := values["body"].Interface(); got == nil {
		t.Error("expected the event to supply a body")
	}
}

func TestWebhookRejectsBadSignatures(t *testing.T) {
	t.Setenv(testSecretEnv, testSecret)
	w := build(t, WebhookName, config(authWith)).(*webhook)

	body := `{"ref":"refs/heads/main"}`
	cases := map[string]*http.Request{
		"no header":       post(body, "", ""),
		"missing prefix":  post(body, "X-Hub-Signature-256", strings.TrimPrefix(signed(testSecret, body), "sha256=")),
		"wrong secret":    post(body, "X-Hub-Signature-256", signed("not-the-secret", body)),
		"not hex":         post(body, "X-Hub-Signature-256", "sha256=zzzz"),
		"signs othe body": post(body, "X-Hub-Signature-256", signed(testSecret, "a different body")),
	}

	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := w.Values(r)
			if err == nil {
				t.Fatal("expected the event to be rejected")
			}
			nerr, ok := err.(*node.NodeError)
			if !ok {
				t.Fatalf("error is %T, want *node.NodeError", err)
			}
			if nerr.Code != CodeUnauthorized {
				t.Errorf("Code = %q, want %q", nerr.Code, CodeUnauthorized)
			}
			// Every rejection reads the same to the caller: which half of the
			// signature was wrong is the operator's business, not the sender's.
			if nerr.Message != "unauthorized" {
				t.Errorf("Message = %q, want %q: the caller must not learn why", nerr.Message, "unauthorized")
			}
		})
	}
}

// An unset secret is a broken daemon, not an unauthorized caller, and the two
// must not report the same thing: one is fixed by the operator, the other by
// the sender.
func TestWebhookUnsetSecretIsInternalNotUnauthorized(t *testing.T) {
	t.Setenv(testSecretEnv, "")
	w := build(t, WebhookName, config(authWith)).(*webhook)

	body := "{}"
	_, err := w.Values(post(body, "X-Hub-Signature-256", signed(testSecret, body)))
	nerr, ok := err.(*node.NodeError)
	if !ok {
		t.Fatalf("error is %T, want *node.NodeError", err)
	}
	if nerr.Code != "secret_unset" {
		t.Errorf("Code = %q, want %q", nerr.Code, "secret_unset")
	}
	if nerr.Kind != node.KindInternal {
		t.Errorf("Kind = %q, want %q", nerr.Kind, node.KindInternal)
	}
	if !strings.Contains(nerr.Message, testSecretEnv) {
		t.Errorf("Message = %q, want it to name the unset variable", nerr.Message)
	}
}

// A webhook with no auth block stays open, which is the V0 default. The point
// of the reporting method is that the daemon can say so out loud.
func TestWebhookReportsWhetherItAuthenticates(t *testing.T) {
	open := build(t, WebhookName, "path: /hooks/deploy").(*webhook)
	if open.Authenticated() {
		t.Error("a webhook with no auth block must report that it authenticates nothing")
	}

	t.Setenv(testSecretEnv, testSecret)
	guarded := build(t, WebhookName, config(authWith)).(*webhook)
	if !guarded.Authenticated() {
		t.Error("a webhook with an auth block must report that it authenticates")
	}
}

// The body cap is applied before the signature is checked, because the
// signature is computed over the body and an oversized one must not be read
// into memory to find that out.
func TestWebhookSizeCapAppliesBeforeAuth(t *testing.T) {
	t.Setenv(testSecretEnv, testSecret)
	w := build(t, WebhookName, config(authWith+"max_body: 8\n")).(*webhook)

	body := strings.Repeat("x", 64)
	_, err := w.Values(post(body, "X-Hub-Signature-256", signed(testSecret, body)))
	nerr, ok := err.(*node.NodeError)
	if !ok {
		t.Fatalf("error is %T, want *node.NodeError", err)
	}
	if nerr.Code != "body_too_large" {
		t.Errorf("Code = %q, want %q", nerr.Code, "body_too_large")
	}
}

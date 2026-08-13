package trigger

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// WebhookName is the capability a pipeline references with
// "uses: trigger.webhook".
const WebhookName = "trigger.webhook"

// DefaultMaxBodyBytes bounds how much of a request body one event carries. It
// exists because the body arrives from outside: without a cap, one caller can
// make the daemon allocate until it dies.
const DefaultMaxBodyBytes = 1 << 20

// webhookMethods is what a webhook may listen for. Restricting the set catches
// a typo at compile time, where an arbitrary string would only ever show up as
// a route that never fires.
var webhookMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// webhookConfig is a trigger.webhook `with` block.
type webhookConfig struct {
	// Path is the route this pipeline answers, for example "/hooks/deploy".
	Path string `yaml:"path"`
	// Method defaults to POST, which is what a webhook almost always is.
	Method string `yaml:"method"`
	// MaxBody overrides DefaultMaxBodyBytes.
	MaxBody int64 `yaml:"max_body"`
	// Auth, when set, requires every event to carry a valid signature. Absent
	// means the route is open to anyone who can reach the listener.
	Auth *authConfig `yaml:"auth"`
}

// WebhookDefinition is the "trigger.webhook" capability: a pipeline runs once
// per matching HTTP request, and the request's parts become its inputs.
//
// The trigger does not own a listener. One socket serves every webhook pipeline
// in the process and the daemon routes by claim, which is why this implements
// HTTPDriven rather than SelfDriven.
func WebhookDefinition() Definition {
	return Definition{Name: WebhookName, New: newWebhook}
}

func newWebhook(cfg node.Config) (Trigger, error) {
	var c webhookConfig
	if err := cfg.Decode(&c); err != nil {
		return nil, err
	}
	if c.Path == "" {
		return nil, node.Errf(node.KindInvalidInput, "missing_path",
			"trigger.webhook requires a path such as \"/hooks/deploy\"")
	}
	if !strings.HasPrefix(c.Path, "/") {
		return nil, node.Errf(node.KindInvalidInput, "bad_path",
			"path %q must start with \"/\"", c.Path)
	}

	method := http.MethodPost
	if c.Method != "" {
		method = strings.ToUpper(c.Method)
		if !slices.Contains(webhookMethods, method) {
			return nil, node.Errf(node.KindInvalidInput, "bad_method",
				"unknown method %q (accepted: %s)", c.Method, strings.Join(webhookMethods, ", "))
		}
	}

	maxBody := c.MaxBody
	switch {
	case maxBody < 0:
		return nil, node.Errf(node.KindInvalidInput, "bad_max_body",
			"max_body must not be negative, got %d", maxBody)
	case maxBody == 0:
		maxBody = DefaultMaxBodyBytes
	}

	auth, err := c.Auth.compile(WebhookName)
	if err != nil {
		return nil, err
	}

	return &webhook{
		path:    c.Path,
		method:  method,
		maxBody: maxBody,
		auth:    auth,
		desc: Descriptor{
			Name: WebhookName,
			Provides: []node.PortDescriptor{
				{Name: "body", Type: node.TypeOf[*types.Bytes]()},
				{Name: "method", Type: node.TypeOf[*types.Text]()},
				{Name: "path", Type: node.TypeOf[*types.Text]()},
				{Name: "query", Type: node.TypeOf[*types.Text]()},
			},
		},
	}, nil
}

// webhook deliberately does not provide the request as a single value. The
// engine already has an HTTPRequest type, but it describes a call to make, and
// an inbound request is not one: sharing the type would let a pipeline forward
// whatever arrived straight back out, which is the confusion a nominal type
// system exists to prevent.
type webhook struct {
	path    string
	method  string
	maxBody int64
	// auth is nil when the route is unauthenticated, which is the default and
	// is why SECURITY.md tells an operator to front the listener.
	auth *verifier
	desc Descriptor
}

// Authenticated reports whether this webhook verifies a signature. The daemon
// uses it to warn about open routes at startup rather than leaving the fact
// buried in a pipeline file.
func (w *webhook) Authenticated() bool { return w.auth != nil }

func (w *webhook) Descriptor() Descriptor { return w.desc }

func (w *webhook) Claim() Claim {
	return Claim{Kind: "http", Key: w.method + " " + w.path}
}

// ResponseTypes are the two payload types that are already a response body.
// Anything else is a structure, and turning a structure into bytes is a step's
// job: a pipeline answering JSON ends in json.encode, which is what keeps
// encoding/json out of the engine.
func (w *webhook) ResponseTypes() []node.TypeID {
	return []node.TypeID{node.TypeOf[*types.Bytes](), node.TypeOf[*types.Text]()}
}

// Respond writes the run's output as the body. The compiler proved the value is
// one of ResponseTypes, so an unexpected type here is an engine bug rather than
// a pipeline error.
func (w *webhook) Respond(rw http.ResponseWriter, value node.Value) error {
	var body []byte
	switch payload := value.Interface().(type) {
	case *types.Bytes:
		body = payload.Value
	case *types.Text:
		body = []byte(payload.Value)
	case nil:
		return nil
	default:
		return node.Errf(node.KindInternal, "unresponsive_type",
			"cannot write a %s as a response body", value.Type())
	}
	_, err := rw.Write(body)
	return err
}

// Values reads the request into the values one event supplies. Reading the body
// here, before any step runs, is what lets an oversized request be refused
// without starting a run.
func (w *webhook) Values(r *http.Request) (map[string]node.Value, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, w.maxBody+1))
	if err != nil {
		return nil, node.Wrap(err, node.KindInvalidInput, "body_unreadable",
			"reading the request body")
	}
	if int64(len(body)) > w.maxBody {
		return nil, node.Errf(node.KindInvalidInput, "body_too_large",
			"request body is larger than %d bytes", w.maxBody)
	}

	// Authentication comes after the size cap and before anything else: the
	// signature is computed over the raw body, so the body must be read first,
	// and nothing beyond reading it should happen for an event that turns out
	// not to be authentic.
	if w.auth != nil {
		if reason, err := w.auth.verify(r, body); err != nil {
			if reason != "" {
				err.Cause = fmt.Errorf("%s", reason)
			}
			return nil, err
		}
	}

	return map[string]node.Value{
		"body":   node.NewValue(&types.Bytes{Value: body}),
		"method": node.NewValue(&types.Text{Value: r.Method}),
		"path":   node.NewValue(&types.Text{Value: r.URL.Path}),
		"query":  node.NewValue(&types.Text{Value: r.URL.RawQuery}),
	}, nil
}

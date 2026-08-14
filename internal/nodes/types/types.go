// Package types holds the domain types that travel along p6e edges.
//
// They live together rather than in the node packages that produce them so
// that a consumer does not have to import a producer: json.decode takes Bytes
// without importing the http package that often produces them.
//
// Every type here is used through a pointer. An interface holding a pointer
// costs no allocation on an edge; one holding a struct allocates on every edge
// (ADR 0001). The wrapper structs exist for that reason: an edge carrying
// *Bytes allocates nothing, while one carrying a bare []byte would.
//
// Values are immutable once produced. Fan-out gives every dependent the same
// pointer, so mutating one in place corrupts a sibling's input.
package types

import (
	"net/http"
	"net/url"
	"time"

	"github.com/arhuman/p6e/internal/node"
)

// Bytes is a raw payload: an HTTP body, a file, a command's output.
type Bytes struct {
	Value []byte
}

// Text is a string payload.
type Text struct {
	Value string
}

// Bool is a truth value, typically a condition's verdict.
type Bool struct {
	Value bool
}

// Int is a whole number.
type Int struct {
	Value int64
}

// Document is a decoded JSON document. Root holds whatever the document was:
// map[string]any, []any, or a scalar.
type Document struct {
	Root any
}

// CheckedURL is an absolute http or https URL that has been through
// NewCheckedURL. It is a distinct type rather than a string so that "this URL
// was validated" is a property the compiler enforces instead of a convention
// two call sites happen to follow.
//
// The reason it earns a type: a Request can be built from data. http.from_url
// takes its URL off an edge, and that edge can carry a webhook body, so the
// check is the difference between a call the pipeline author chose and one an
// attacker did. Spreading that check across every node that builds a Request
// means the next such node can omit it and nothing will say so.
//
// The zero value is the empty URL, which every consumer rejects. That is the
// one gap a value type cannot close in Go, since an empty composite literal is
// always constructible.
type CheckedURL struct {
	// value is unexported so the only way to a non-empty CheckedURL is through
	// NewCheckedURL.
	value string
}

// NewCheckedURL parses raw and reports whether it is usable as a request
// target. capability names the node in the error, since the same check runs at
// compile time for a configured URL and at execution for one that arrived on an
// edge.
//
// It checks the shape of the URL, not where it points. The destination policy
// is enforced in the dialer, on the address actually being connected to, so
// that a hostname cannot resolve to one address here and another at connect
// time (ADR 0014).
func NewCheckedURL(capability, raw string) (CheckedURL, *node.Error) {
	if raw == "" {
		return CheckedURL{}, node.Errf(node.KindInvalidInput, "missing_url",
			"%s requires a url", capability)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return CheckedURL{}, node.Wrap(err, node.KindInvalidInput, "bad_url",
			"invalid url %q", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return CheckedURL{}, node.Errf(node.KindInvalidInput, "bad_url",
			"url %q must use the http or https scheme", raw)
	}
	if parsed.Host == "" {
		return CheckedURL{}, node.Errf(node.KindInvalidInput, "bad_url",
			"url %q has no host", raw)
	}
	return CheckedURL{value: raw}, nil
}

// String returns the URL as given. It is also what makes CheckedURL print
// readably in an error or a log line.
func (u CheckedURL) String() string { return u.value }

// Request describes an HTTP call to make.
//
// URL is a CheckedURL rather than a string, so a Request cannot exist unless
// its target went through NewCheckedURL. Headers and Body carry whatever the
// pipeline put there and are not the engine's business.
type Request struct {
	Method  string
	URL     CheckedURL
	Headers map[string]string
	Body    []byte
}

// Response is what an HTTP call returned. A non-2xx status is data, not an
// error: whether a 404 is a problem is the workflow's decision.
type Response struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// Time is an instant, such as when a schedule fired.
type Time struct {
	Value time.Time
}

// Command describes a local process to run.
type Command struct {
	Name string
	Args []string
	// Dir is the working directory, empty for the engine's own.
	Dir string
	// Timeout bounds the run, zero for no bound beyond the execution's context.
	Timeout time.Duration
}

// CommandResult is what a process did. A non-zero exit code is data, for the
// same reason a 404 is: only the workflow knows whether it means failure.
type CommandResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

func init() {
	node.RegisterType[*Bytes]("Bytes")
	node.RegisterType[*Text]("Text")
	node.RegisterType[*Bool]("Bool")
	node.RegisterType[*Int]("Int")
	node.RegisterType[*Time]("Time")
	node.RegisterType[*Document]("JSONDocument")
	node.RegisterType[*Request]("HTTPRequest")
	node.RegisterType[*Response]("HTTPResponse")
	node.RegisterType[*Command]("Command")
	node.RegisterType[*CommandResult]("CommandResult")
}

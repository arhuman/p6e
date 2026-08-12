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

// Request describes an HTTP call to make.
type Request struct {
	Method  string
	URL     string
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
	node.RegisterType[*Document]("JSONDocument")
	node.RegisterType[*Request]("HTTPRequest")
	node.RegisterType[*Response]("HTTPResponse")
	node.RegisterType[*Command]("Command")
	node.RegisterType[*CommandResult]("CommandResult")
}

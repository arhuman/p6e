package node

import (
	"context"
	"errors"
	"fmt"
)

// ErrorKind classifies what went wrong, in the only terms the engine's policy
// layer understands. A node reports the kind; the workflow decides what to do
// about it.
type ErrorKind string

const (
	// KindTransient: the same call might succeed if repeated. Network timeouts,
	// 503s, lock contention.
	KindTransient ErrorKind = "transient"
	// KindPermanent: repeating the call will fail the same way. A 404, a
	// missing binary, a rejected credential.
	KindPermanent ErrorKind = "permanent"
	// KindInvalidInput: the input the node received is not usable. This is a
	// bug in the pipeline, not a condition in the world.
	KindInvalidInput ErrorKind = "invalid_input"
	// KindCancelled: the execution was cancelled or its deadline expired.
	KindCancelled ErrorKind = "cancelled"
	// KindInternal: the node or the engine broke. Recovered panics land here.
	KindInternal ErrorKind = "internal"
)

// NodeError is the normalized failure every node reports. Arbitrary Go errors
// from a library never reach workflow semantics: they are wrapped here first,
// so the policy layer has a fixed vocabulary to act on.
type NodeError struct {
	// Code is a node-specific identifier, stable enough to match on, for
	// example "http_status" or "exec_not_found".
	Code string
	// Kind drives engine policy.
	Kind ErrorKind
	// Message is human-facing.
	Message string
	// Retryable states whether the engine may retry. It defaults to true only
	// for KindTransient; a node can override it when it knows better, for
	// instance a 429 that carries a Retry-After.
	Retryable bool
	// Cause is the underlying Go error, if any. It is for diagnostics; policy
	// must not inspect it.
	Cause error
}

// Error implements error.
func (e *NodeError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s [%s/%s]: %v", e.Message, e.Kind, e.Code, e.Cause)
	}
	return fmt.Sprintf("%s [%s/%s]", e.Message, e.Kind, e.Code)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *NodeError) Unwrap() error { return e.Cause }

// Errf builds a NodeError. Retryable is derived from kind; assign the field
// afterwards to override it.
func Errf(kind ErrorKind, code, format string, args ...any) *NodeError {
	return &NodeError{
		Code:      code,
		Kind:      kind,
		Message:   fmt.Sprintf(format, args...),
		Retryable: kind == KindTransient,
	}
}

// Wrap builds a NodeError around an existing Go error. This is the boundary
// where library errors stop being library errors.
func Wrap(err error, kind ErrorKind, code, format string, args ...any) *NodeError {
	e := Errf(kind, code, format, args...)
	e.Cause = err
	return e
}

// Normalize converts an arbitrary Go error into a NodeError, so a node that
// simply forwards an error still produces something policy can read.
// Cancellation and deadlines are recognized; everything else is permanent
// until a node says otherwise, because guessing that an unknown failure is
// retryable turns one failure into several.
func Normalize(err error, code string) *NodeError {
	if err == nil {
		return nil
	}
	if ne, ok := errors.AsType[*NodeError](err); ok {
		return ne
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Wrap(err, KindCancelled, "cancelled", "execution cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(err, KindCancelled, "deadline_exceeded", "deadline exceeded")
	default:
		return Wrap(err, KindPermanent, code, "%v", err)
	}
}

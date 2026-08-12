package node

import "time"

// ResultMeta is what the engine records about an execution, as opposed to what
// the node computed.
type ResultMeta struct {
	// Duration is the wall time of the attempt that produced this result.
	Duration time.Duration
	// Attempt is 1 for the first try, 2 for the first retry, and so on.
	Attempt int
}

// Result is a node's typed outcome. Exactly one of Value and Err is the logical
// outcome: if Err is non-nil, Value is meaningless.
type Result[T any] struct {
	Value T
	Err   *NodeError
	Meta  ResultMeta
}

// Ok returns a successful result.
func Ok[T any](v T) Result[T] { return Result[T]{Value: v} }

// Fail returns a failed result.
func Fail[T any](err *NodeError) Result[T] { return Result[T]{Err: err} }

// ResultValue is Result with the type parameter erased: what the plan and the
// executor actually move around.
type ResultValue struct {
	Value Value
	Err   *NodeError
	Meta  ResultMeta
}

// Failed reports whether the step did not produce a value.
func (r ResultValue) Failed() bool { return r.Err != nil }

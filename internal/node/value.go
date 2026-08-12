package node

// Value is what travels along an edge: a typed Go reference plus its runtime
// type identity. It is not a serialized document and never becomes one. Fan-out
// hands the same Value to every downstream step, so a large payload is shared,
// not copied.
//
// Per ADR 0001, domain types on edges should be pointers. An interface holding
// a pointer costs no allocation; one holding a struct allocates on every edge.
//
// Values are immutable by convention. Two downstream nodes hold the same
// reference, so mutating a payload in place corrupts a sibling's input. A node
// that needs to change something produces a new value.
type Value struct {
	typ TypeID
	ref any
}

// NewValue boxes a typed Go value. The TypeID comes from T, so a Value cannot
// be built carrying a type identity that contradicts its contents.
func NewValue[T any](v T) Value {
	return Value{typ: TypeOf[T](), ref: v}
}

// Type reports the value's runtime type identity.
func (v Value) Type() TypeID { return v.typ }

// IsZero reports whether v carries nothing. A zero Value is what an unexecuted
// step's result slot holds.
func (v Value) IsZero() bool { return v.ref == nil && v.typ == "" }

// Interface exposes the underlying reference for inspection and reporting, for
// example by the CLI. Execution paths must not use it: they go through the
// typed adapter, which asserts back into typed code.
func (v Value) Interface() any { return v.ref }

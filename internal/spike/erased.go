package spike

import (
	"context"
	"fmt"
)

// Candidate 1: generics plus an erased runtime adapter.
//
// Node authors write a typed function. The adapter erases it once, behind a
// non-generic interface the plan can hold in a slice. Every edge then costs one
// type assertion to get back into typed code, plus boxing the output into a
// Value on the way out.

// Value carries a typed Go reference plus its runtime type identity. It is not
// serialized: ptr holds the concrete value itself.
type Value struct {
	Type TypeID
	ptr  any
}

// ResultValue is the erased counterpart of Result.
type ResultValue struct {
	Value Value
	Err   error
}

// ErasedNode is what the plan stores: the type parameters are gone.
type ErasedNode interface {
	In() TypeID
	Out() TypeID
	Execute(ctx context.Context, in Value) ResultValue
}

type erasedNode[I, O any] struct {
	fn    func(context.Context, I) Result[O]
	inID  TypeID
	outID TypeID
}

// NewErasedNode is the authoring entry point: a typed function goes in, an
// interface the runtime can hold comes out.
func NewErasedNode[I, O any](inID, outID TypeID, fn func(context.Context, I) Result[O]) ErasedNode {
	return &erasedNode[I, O]{fn: fn, inID: inID, outID: outID}
}

func (n *erasedNode[I, O]) In() TypeID  { return n.inID }
func (n *erasedNode[I, O]) Out() TypeID { return n.outID }

func (n *erasedNode[I, O]) Execute(ctx context.Context, in Value) ResultValue {
	typed, ok := in.ptr.(I)
	if !ok {
		return ResultValue{Err: fmt.Errorf("spike: input is %s, node wants %s", in.Type, n.inID)}
	}
	r := n.fn(ctx, typed)
	if r.Err != nil {
		return ResultValue{Err: r.Err}
	}
	return ResultValue{Value: Value{Type: n.outID, ptr: r.Value}}
}

// RunErased walks a compiled chain. This is the hot path under measurement.
func RunErased(ctx context.Context, chain []ErasedNode, in Value) (Value, error) {
	cur := in
	for _, n := range chain {
		r := n.Execute(ctx, cur)
		if r.Err != nil {
			return Value{}, r.Err
		}
		cur = r.Value
	}
	return cur, nil
}

// BuildErasedChain builds a chain of n identity-ish nodes over Payload.
func BuildErasedChain(n int) []ErasedNode {
	chain := make([]ErasedNode, n)
	for i := range chain {
		chain[i] = NewErasedNode("Payload", "Payload", bump)
	}
	return chain
}

// BuildErasedChainPtr builds the same chain over *Payload, where boxing a
// pointer into an interface does not allocate.
func BuildErasedChainPtr(n int) []ErasedNode {
	chain := make([]ErasedNode, n)
	for i := range chain {
		chain[i] = NewErasedNode("*Payload", "*Payload", bumpPtr)
	}
	return chain
}

package spike

import (
	"context"
	"fmt"
)

// Candidate 3: compile-time composition into typed closures.
//
// Instead of erasing at every edge, the plan builder folds the chain into a
// single closure. Each fold does one type assertion at build time, so at run
// time interior edges pass typed Go values as ordinary arguments: no assertion,
// no boxing. Only the chain's entry and exit touch a Value.
//
// The accumulator is always func(context.Context, Value) Result[T] for the
// current tail type T. That shape is expressible in a type assertion inside a
// generic method, which is what makes dynamic composition possible at all.

// ComposableNode is the authoring interface for this candidate.
type ComposableNode interface {
	// Head returns an accumulator that unboxes the pipeline source into this
	// node's input type. Only the first node in a chain provides it.
	Head() any
	// AppendTo folds this node onto acc, returning a new accumulator.
	AppendTo(acc any) (any, error)
	// Seal erases the finished accumulator back to a callable the plan holds.
	Seal(acc any) (func(context.Context, Value) ResultValue, error)
	Out() TypeID
}

type composedNode[I, O any] struct {
	fn    func(context.Context, I) Result[O]
	inID  TypeID
	outID TypeID
}

// NewComposableNode wraps a typed node function for compile-time composition.
func NewComposableNode[I, O any](inID, outID TypeID, fn func(context.Context, I) Result[O]) ComposableNode {
	return composedNode[I, O]{fn: fn, inID: inID, outID: outID}
}

func (n composedNode[I, O]) Out() TypeID { return n.outID }

func (n composedNode[I, O]) Head() any {
	inID := n.inID
	return func(_ context.Context, src Value) Result[I] {
		v, ok := src.ptr.(I)
		if !ok {
			return Result[I]{Err: fmt.Errorf("spike: source is %s, chain wants %s", src.Type, inID)}
		}
		return Result[I]{Value: v}
	}
}

func (n composedNode[I, O]) AppendTo(acc any) (any, error) {
	prev, ok := acc.(func(context.Context, Value) Result[I])
	if !ok {
		return nil, fmt.Errorf("spike: cannot append node wanting %s to chain producing %T", n.inID, acc)
	}
	fn := n.fn
	return func(ctx context.Context, src Value) Result[O] {
		r := prev(ctx, src)
		if r.Err != nil {
			return Result[O]{Err: r.Err}
		}
		return fn(ctx, r.Value)
	}, nil
}

func (n composedNode[I, O]) Seal(acc any) (func(context.Context, Value) ResultValue, error) {
	final, ok := acc.(func(context.Context, Value) Result[O])
	if !ok {
		return nil, fmt.Errorf("spike: cannot seal chain producing %T as %s", acc, n.outID)
	}
	outID := n.outID
	return func(ctx context.Context, src Value) ResultValue {
		r := final(ctx, src)
		if r.Err != nil {
			return ResultValue{Err: r.Err}
		}
		return ResultValue{Value: Value{Type: outID, ptr: r.Value}}
	}, nil
}

// Compose folds a chain into one callable. All type checking happens here,
// once, at plan build time.
func Compose(chain []ComposableNode) (func(context.Context, Value) ResultValue, error) {
	if len(chain) == 0 {
		return nil, fmt.Errorf("spike: empty chain")
	}
	acc := chain[0].Head()
	for i, n := range chain {
		next, err := n.AppendTo(acc)
		if err != nil {
			return nil, fmt.Errorf("spike: step %d: %w", i, err)
		}
		acc = next
	}
	return chain[len(chain)-1].Seal(acc)
}

// BuildComposedChain builds and folds a chain of n identity-ish nodes.
func BuildComposedChain(n int) func(context.Context, Value) ResultValue {
	chain := make([]ComposableNode, n)
	for i := range chain {
		chain[i] = NewComposableNode("Payload", "Payload", bump)
	}
	fused, err := Compose(chain)
	if err != nil {
		panic(err)
	}
	return fused
}

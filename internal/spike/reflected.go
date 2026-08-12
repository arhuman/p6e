package spike

import (
	"context"
	"fmt"
	"reflect"
)

// Candidate 2: reflection.
//
// The plan holds reflect.Value function handles and calls them dynamically.
// Node authors write nothing special, and the engine needs no generics, but
// every edge pays for reflect.Call: argument slice allocation, boxing, and
// result field extraction.

// ReflectNode invokes a func(context.Context, I) Result[O] through reflection.
type ReflectNode struct {
	fn     reflect.Value
	inType reflect.Type
}

// NewReflectNode accepts any func(context.Context, I) Result[O].
func NewReflectNode(fn any) (*ReflectNode, error) {
	v := reflect.ValueOf(fn)
	t := v.Type()
	if t.Kind() != reflect.Func || t.NumIn() != 2 || t.NumOut() != 1 {
		return nil, fmt.Errorf("spike: want func(context.Context, I) Result[O], got %s", t)
	}
	return &ReflectNode{fn: v, inType: t.In(1)}, nil
}

// Execute calls the node. in must hold a value assignable to the node's input.
func (n *ReflectNode) Execute(ctx context.Context, in any) (any, error) {
	av := reflect.ValueOf(in)
	if !av.Type().AssignableTo(n.inType) {
		return nil, fmt.Errorf("spike: input is %s, node wants %s", av.Type(), n.inType)
	}
	out := n.fn.Call([]reflect.Value{reflect.ValueOf(ctx), av})
	res := out[0]
	if err := res.Field(1).Interface(); err != nil {
		return nil, err.(error)
	}
	return res.Field(0).Interface(), nil
}

// RunReflect walks a compiled chain. This is the hot path under measurement.
func RunReflect(ctx context.Context, chain []*ReflectNode, in any) (any, error) {
	cur := in
	for _, n := range chain {
		out, err := n.Execute(ctx, cur)
		if err != nil {
			return nil, err
		}
		cur = out
	}
	return cur, nil
}

// BuildReflectChain builds a chain of n identity-ish nodes over Payload.
func BuildReflectChain(n int) []*ReflectNode {
	chain := make([]*ReflectNode, n)
	for i := range chain {
		node, err := NewReflectNode(bump)
		if err != nil {
			panic(err)
		}
		chain[i] = node
	}
	return chain
}

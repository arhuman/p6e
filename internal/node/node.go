package node

import "context"

// ExecutionContext is the execution-scoped information a node may need, kept
// deliberately out of the business payload. Cancellation and deadlines travel
// separately, in the standard context.Context.
//
// It is passed to nodes by pointer and is read-only for them. Copying it by
// value costs 16ns per edge, more than the rest of the adapter put together
// (ADR 0001). The executor owns one per step and updates Attempt between
// attempts, which are sequential, so a node never sees it change underfoot.
type ExecutionContext struct {
	WorkflowID  string
	ExecutionID string
	StepID      string
	Attempt     int
}

// RuntimeNode is what a compiled plan holds: a node with its type parameters
// erased, so nodes of unrelated signatures fit in one slice.
//
// Implementations must be safe for concurrent use. One instance serves many
// workflows, executions, and steps at once. Infrastructure state such as an
// HTTP connection pool belongs here; workflow or business state does not.
//
// inputs is positional and matches Descriptor().Inputs. The compiler has
// already proven each input's type, so Execute may assume it.
type RuntimeNode interface {
	Descriptor() Descriptor
	Execute(ctx context.Context, ec *ExecutionContext, inputs []Value) ResultValue
}

// NewSource adapts a node that takes no pipeline input, such as a constant or
// a step configured entirely by its with block.
func NewSource[O any](name string, fn func(context.Context, *ExecutionContext) Result[O]) RuntimeNode {
	return &sourceNode[O]{
		desc: Descriptor{Name: name, Output: PortDescriptor{Name: "out", Type: TypeOf[O]()}},
		fn:   fn,
	}
}

// NewTypedNode adapts the common case: one typed input, one typed output.
// This is the authoring entry point for node implementers, and the only place
// where type erasure happens (ADR 0001).
func NewTypedNode[I, O any](name string, fn func(context.Context, *ExecutionContext, I) Result[O]) RuntimeNode {
	return &typedNode[I, O]{
		desc: Descriptor{
			Name:   name,
			Inputs: []PortDescriptor{{Name: "in", Type: TypeOf[I]()}},
			Output: PortDescriptor{Name: "out", Type: TypeOf[O]()},
		},
		fn: fn,
	}
}

// NewTypedNode2 adapts a fan-in node: two typed inputs, bound in the order the
// step's needs list declares them.
func NewTypedNode2[I1, I2, O any](name string, fn func(context.Context, *ExecutionContext, I1, I2) Result[O]) RuntimeNode {
	return &typedNode2[I1, I2, O]{
		desc: Descriptor{
			Name: name,
			Inputs: []PortDescriptor{
				{Name: "in0", Type: TypeOf[I1]()},
				{Name: "in1", Type: TypeOf[I2]()},
			},
			Output: PortDescriptor{Name: "out", Type: TypeOf[O]()},
		},
		fn: fn,
	}
}

type sourceNode[O any] struct {
	desc Descriptor
	fn   func(context.Context, *ExecutionContext) Result[O]
}

func (n *sourceNode[O]) Descriptor() Descriptor { return n.desc }

func (n *sourceNode[O]) Execute(ctx context.Context, ec *ExecutionContext, inputs []Value) ResultValue {
	if len(inputs) != 0 {
		return ResultValue{Err: arityError(&n.desc, len(inputs))}
	}
	return erase(n.fn(ctx, ec), n.desc.Output.Type)
}

type typedNode[I, O any] struct {
	desc Descriptor
	fn   func(context.Context, *ExecutionContext, I) Result[O]
}

func (n *typedNode[I, O]) Descriptor() Descriptor { return n.desc }

func (n *typedNode[I, O]) Execute(ctx context.Context, ec *ExecutionContext, inputs []Value) ResultValue {
	if len(inputs) != 1 {
		return ResultValue{Err: arityError(&n.desc, len(inputs))}
	}
	in, ok := inputs[0].ref.(I)
	if !ok {
		return ResultValue{Err: typeError(&n.desc, 0, inputs[0].typ)}
	}
	return erase(n.fn(ctx, ec, in), n.desc.Output.Type)
}

type typedNode2[I1, I2, O any] struct {
	desc Descriptor
	fn   func(context.Context, *ExecutionContext, I1, I2) Result[O]
}

func (n *typedNode2[I1, I2, O]) Descriptor() Descriptor { return n.desc }

func (n *typedNode2[I1, I2, O]) Execute(ctx context.Context, ec *ExecutionContext, inputs []Value) ResultValue {
	if len(inputs) != 2 {
		return ResultValue{Err: arityError(&n.desc, len(inputs))}
	}
	in0, ok := inputs[0].ref.(I1)
	if !ok {
		return ResultValue{Err: typeError(&n.desc, 0, inputs[0].typ)}
	}
	in1, ok := inputs[1].ref.(I2)
	if !ok {
		return ResultValue{Err: typeError(&n.desc, 1, inputs[1].typ)}
	}
	return erase(n.fn(ctx, ec, in0, in1), n.desc.Output.Type)
}

// erase converts a typed result back into the plan's currency. The output
// TypeID is taken from the descriptor rather than recomputed, keeping TypeOf
// off the hot path.
func erase[O any](r Result[O], outType TypeID) ResultValue {
	if r.Err != nil {
		return ResultValue{Err: r.Err, Meta: r.Meta}
	}
	return ResultValue{Value: Value{typ: outType, ref: r.Value}, Meta: r.Meta}
}

// typeError and arityError take the descriptor by pointer and are called only
// when a check fails. Passing a Descriptor by value costs 72 bytes of copying,
// which on the happy path is more than the rest of the adapter (ADR 0001).
//
// Both report internal rather than invalid_input: the compiler proved these
// cannot happen, so reaching one is an engine bug, and blaming the workflow
// author would send the reader looking in the wrong place.
func typeError(desc *Descriptor, port int, got TypeID) *NodeError {
	return Errf(KindInternal, "type_mismatch",
		"node %q input %q expects %s but received %s",
		desc.Name, desc.Inputs[port].Name, desc.Inputs[port].Type, got)
}

func arityError(desc *Descriptor, got int) *NodeError {
	return Errf(KindInternal, "arity_mismatch",
		"node %q expects %d input(s), received %d", desc.Name, len(desc.Inputs), got)
}

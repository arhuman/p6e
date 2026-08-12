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
//
// The pointer is valid only for the duration of the Execute call. A node must
// not retain it: the executor writes Attempt before the next attempt, so a
// goroutine the node left running would race with that write and read an
// attempt number belonging to a later try. Copy any field you need to keep.
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

// NewTypedNodeN adapts a node whose arity comes from its configuration: any
// number of inputs, all of one type, with port names supplied by the caller.
// It is the general form of NewTypedNode2, for nodes such as a template with
// one port per placeholder.
//
// Every port carries the same type, which is what lets one slice hold them. A
// node mixing input types needs its own adapter.
//
// Port names are the pipeline-facing contract: they are what a step's named
// needs mapping binds against, and because the ports share a type, ADR 0009
// requires that mapping whenever there is more than one.
func NewTypedNodeN[I, O any](name string, ports []string, fn func(context.Context, *ExecutionContext, []I) Result[O]) RuntimeNode {
	typ := TypeOf[I]()
	inputs := make([]PortDescriptor, len(ports))
	for i, p := range ports {
		inputs[i] = PortDescriptor{Name: p, Type: typ}
	}
	return &typedNodeN[I, O]{
		desc: Descriptor{
			Name:   name,
			Inputs: inputs,
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

type typedNodeN[I, O any] struct {
	desc Descriptor
	fn   func(context.Context, *ExecutionContext, []I) Result[O]
}

func (n *typedNodeN[I, O]) Descriptor() Descriptor { return n.desc }

func (n *typedNodeN[I, O]) Execute(ctx context.Context, ec *ExecutionContext, inputs []Value) ResultValue {
	if len(inputs) != len(n.desc.Inputs) {
		return ResultValue{Err: arityError(&n.desc, len(inputs))}
	}
	// One slice per call, because the typed values cannot alias the erased
	// ones. A node's arity here is a handful of ports, not a hot-path array.
	args := make([]I, len(inputs))
	for i := range inputs {
		in, ok := inputs[i].ref.(I)
		if !ok {
			return ResultValue{Err: typeError(&n.desc, i, inputs[i].typ)}
		}
		args[i] = in
	}
	return erase(n.fn(ctx, ec, args), n.desc.Output.Type)
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

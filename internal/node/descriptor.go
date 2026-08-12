package node

import "strings"

// PortDescriptor names one input or output of a node and fixes its type.
type PortDescriptor struct {
	Name string
	Type TypeID
}

// Descriptor is everything the compiler needs to know about a node without
// executing it: what it is called and what types it consumes and produces.
//
// V0 nodes have exactly one output. Multiple outputs would mean edges carrying
// a port selector, which the configuration format does not express yet.
type Descriptor struct {
	// Name is the capability a pipeline references, for example "http.request".
	Name string
	// Inputs are positional: a step's needs list binds to them in order.
	Inputs []PortDescriptor
	// Output is the single value this node produces.
	Output PortDescriptor
}

// Arity is how many inputs the node requires.
func (d Descriptor) Arity() int { return len(d.Inputs) }

// InputTypes renders the input signature for error messages.
func (d Descriptor) InputTypes() string {
	if len(d.Inputs) == 0 {
		return "()"
	}
	parts := make([]string, len(d.Inputs))
	for i, p := range d.Inputs {
		parts[i] = string(p.Type)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

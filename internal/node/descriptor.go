package node

import (
	"strconv"
	"strings"
)

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

// InputNames renders the input port names for error messages, which is what a
// pipeline author needs when binding needs by name.
func (d Descriptor) InputNames() string {
	if len(d.Inputs) == 0 {
		return "none"
	}
	names := make([]string, len(d.Inputs))
	for i, p := range d.Inputs {
		names[i] = p.Name
	}
	return quoteNames(names)
}

// AmbiguousInputs reports whether two input ports share a type, and renders
// their names for an error message.
//
// This is the one binding mistake the type check cannot catch: when two ports
// have the same type, both orders of a positional needs list type check and
// mean different things. The compiler uses this to require the named form for
// such a node, so the ambiguity is unreachable rather than merely documented.
func (d Descriptor) AmbiguousInputs() (TypeID, string, bool) {
	byType := make(map[TypeID][]string, len(d.Inputs))
	for _, p := range d.Inputs {
		byType[p.Type] = append(byType[p.Type], p.Name)
	}
	// Ranging over Inputs rather than the map keeps the report deterministic:
	// the first duplicated type in port order always wins.
	for _, p := range d.Inputs {
		if names := byType[p.Type]; len(names) > 1 {
			return p.Type, quoteNames(names), true
		}
	}
	return "", "", false
}

func quoteNames(names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = strconv.Quote(n)
	}
	return strings.Join(parts, ", ")
}

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

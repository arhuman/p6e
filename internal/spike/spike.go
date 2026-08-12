// Package spike is a throwaway benchmark comparing three ways to bridge
// compile-time Go typing with a dynamically loaded pipeline definition.
//
// It exists to answer one question before the engine is written: what does the
// engine pay, per edge, to hand node A's typed output to node B? The winner is
// ported into internal/node and this package is deleted. See
// docs/adr/0001-type-bridge.md.
package spike

import "context"

// TypeID is the runtime identity of a port type. The compiler compares these;
// the executor trusts them.
type TypeID string

// Payload is a stand-in for a real domain type: big enough that boxing it into
// an interface must heap-allocate, so the erased approach pays a visible cost.
type Payload struct {
	N    int64
	Data []byte
}

// Result is the spike's simplified node outcome. The real engine adds
// NodeError and ResultMeta; neither changes the cost being measured here.
type Result[T any] struct {
	Value T
	Err   error
}

// bump is the node body used by every candidate: cheap enough that the
// benchmark measures the bridge rather than the node.
func bump(_ context.Context, p Payload) Result[Payload] {
	p.N++
	return Result[Payload]{Value: p}
}

func bumpPtr(_ context.Context, p *Payload) Result[*Payload] {
	p.N++
	return Result[*Payload]{Value: p}
}

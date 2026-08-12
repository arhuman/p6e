# 1. Bridging Go's static types to a dynamic pipeline definition

Date: 2026-08-12

## Status

Accepted

## Context

p6e loads pipelines at run time but wants nodes that are statically typed at
author time. Go types are a compile-time construct; a pipeline file is data.
Something has to carry a node's input and output types across that gap, and
whatever it is sits on the hot path: it runs once per edge, on every execution.

The engine's central performance metric is the latency it adds between node A
completing and node B being invoked. That cost is almost entirely this bridge.
Choosing it first, before any engine code exists, is the point of this ADR.

Three candidates were prototyped and benchmarked in `internal/spike` (see the
`perf: benchmark three type-bridge designs` commit; the package is deleted once
the winner is ported into `internal/node`):

1. **Erased runtime adapter.** Authors write `func(context.Context, I) Result[O]`.
   A generic adapter erases it behind a non-generic interface the plan can hold
   in a slice. Each edge costs one interface dispatch, one type assertion back
   into typed code, and boxing the output into a `Value`.
2. **Reflection.** The plan holds `reflect.Value` function handles and calls them
   with `reflect.Call`.
3. **Compile-time composition.** The plan builder folds a chain into a single
   closure, doing one type assertion per step at build time. Interior edges then
   pass typed Go values as ordinary arguments: no assertion, no boxing.

## Measurements

Apple M3 Pro, Go 1.26.2, darwin/arm64. Median of three runs, 100-node identity
chain, so per-edge figures are amortized. The payload is a 32-byte struct
(`{int64, []byte}`) except where noted.

All four candidates were measured on the same toolchain, so the comparison holds
even though the project later pinned Go 1.26.5 and every absolute figure
improved. See the addendum for the current cost of the winner.

| Approach | ns/edge | allocs/edge | Checks types at |
|---|---|---|---|
| Typed function chain (floor, not a viable engine) | 5.3 | 0 | compile time (Go's) |
| **Erased adapter, pointer payload** | **13.2** | **0** | execution |
| Composed/fused chain | 13.3 | 0 (1 per chain) | plan build |
| Erased adapter, value payload | 31.3 | 1 | execution |
| Reflection | 422 | 3 | execution |

Composition costs 4.4 us to build a 100-step chain, paid once per plan.

Two results decided this:

- **Reflection is out by a factor of 32.** It is not a close call and needs no
  further argument.
- **Composition's advantage evaporates when payloads are pointers.** Fusion
  exists to remove the boxing allocation, and 13.3 vs 13.2 ns/edge says a
  pointer payload removes it just as well for free. The remaining 13 ns is
  interface dispatch plus the assertion, which fusion also cannot remove from
  the chain's entry and exit.

## Decision

**Use the erased runtime adapter (candidate 1), and make pointer payloads the
convention for domain types on edges.**

Node authors write typed functions:

```go
func NewTypedNode[I, O any](desc Descriptor, fn func(context.Context, I) Result[O]) RuntimeNode
```

The plan holds `RuntimeNode`, a non-generic interface. A `Value` is a typed Go
reference plus its `TypeID`, never a serialized document. Erasure happens once,
at the runtime boundary, and the compiler has already proven the assertion
inside `Execute` will succeed.

Domain types crossing edges are pointers (`*HTTPResponse`, not `HTTPResponse`).
An interface holding a pointer does not allocate; one holding a struct does.
This is the whole difference between 13 and 31 ns/edge, and it costs nothing but
a convention.

Composition was rejected on numbers, but it would have been rejected on function
regardless: **fusion destroys step boundaries.** The handoff requires per-step
retry policy and per-step result metadata (duration, attempt). You cannot retry
or time an interior node that has been inlined into its neighbours. A fused
chain is also a chain, and p6e is a DAG engine; extending composition to fan-out
and fan-in is a substantially harder design for no measured gain.

## Consequences

### Positive

- 13 ns and zero allocations of engine overhead per edge, with no serialization
  and no reflection on the hot path.
- Step boundaries survive, so retry, cancellation, and per-step metadata are
  expressible.
- Authoring stays statically typed; `any` appears only inside the adapter.
- Uniform for any DAG shape. Fan-out shares one `Value`, so a large payload is
  never copied.

### Negative

- The assertion in `Execute` is a run-time check of something the compiler
  already proved. It is 13 ns of belt-and-braces, and it stays: it is the
  backstop if a registry bug ever lets a mistyped edge through.
- Pointer payloads make immutability a convention rather than a guarantee. Two
  downstream nodes receive the same pointer; the rule that outputs are immutable
  is enforced by review, not the type system.

### Risks

- Node authors who use a value type on a port silently pay an allocation per
  edge. Worth a lint or a documented rule in the node authoring guide.

## Alternatives Considered

Covered above with measurements: reflection (32x slower), compile-time
composition (no measured gain, loses step boundaries), and a raw typed function
chain (the floor, but it forces every node in a pipeline to share one input and
output type, so it is not an engine).

## Addendum: what the port actually cost

Measured on `internal/node` after porting, same machine, one adapter call
through the `RuntimeNode` interface, zero allocations throughout:

| Version | ns/call |
|---|---|
| First working port | 25.4 |
| ExecutionContext passed by pointer | 15.3 |
| Descriptor by pointer, error paths only | (included above) |
| Same code on Go 1.26.5 (the pinned toolchain) | 11.4 |

Two findings worth keeping, because both were invisible until measured and both
cost more than the type assertion the whole ADR is about:

- **Passing `ExecutionContext` by value cost 16ns per edge.** It is three
  strings and an int, 56 bytes, copied on every call. Nodes now receive
  `*ExecutionContext` and treat it as read-only.
- **Passing `Descriptor` by value to the arity and type checks cost 8ns per
  edge**, for 72 bytes copied on a path that only reads a name when something
  has already gone wrong. The checks are inlined into `Execute` and the
  descriptor reaches the error builders by pointer.

The lesson generalizes: on a path this short, struct copies dominate. Anything
wider than a word that crosses the adapter should cross it by pointer.

## Addendum: convention-only immutability also makes retry unsound

The Negative section above notes that pointer payloads make immutability a
convention. It stops short of one consequence, added here because a review found
it and it belongs with the decision that caused it.

`runStep` passes the same input to every retry attempt. A node that partially
mutates its input before failing therefore retries against its own corrupted
data. So the convention does not only protect fan-out siblings: `Retryable` is
sound only for nodes that honour it. A node that mutates its input has a retry
policy that silently means something other than "try again".

ADR 0006 addresses both this and fan-out corruption by making violations
detectable, having rejected prevention as unavailable in Go at an acceptable
cost.

## References

- Handoff sections 28 to 30 (the architectural question, the adapter sketch, the
  performance philosophy).
- `internal/spike` in the `perf: benchmark three type-bridge designs` commit.

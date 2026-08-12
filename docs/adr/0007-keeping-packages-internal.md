# 7. Keeping every package internal for now

Date: 2026-08-12

## Status

Accepted

## Context

The handoff asks for "a clean, minimal, and extensible interface that makes it
straightforward to implement any integration already available in systems like
n8n". A review pointed out the obvious tension: every package lives under
`internal/`, so an out-of-tree author cannot import the node contract at all and
would have to fork the repository. The architecture supports extension; the
package layout forbids it.

The recommendation, mine included, was to promote `internal/node` to the
repository root. Working through it changed my mind.

## Why promoting `node` alone achieves nothing

An external node author needs four things: the contract (`node`), the domain
types their node speaks (`nodes/types`, or nothing they write can connect to a
built-in node), a way to register it, and a way to run the resulting pipeline
(`pipeline.Compile` and `runtime.Run`).

Promoting `node` gives them the first. They can write a perfectly good node,
build a `node.Registry`, and then discover there is no exported way to compile or
execute anything. So the useful unit of promotion is not one package: it is
`node`, `types`, `pipeline`, and `runtime`, plus a decision about whether the
built-in `nodes` registry is public.

That is not a tidying commit. It is publishing the whole engine's API.

## Decision

**Keep every package under `internal/` for V0. Extension means adding a node
in-tree.**

The reasoning is the one that decided ADR 0005, applied consistently. There, the
argument for acting immediately was that a declarative schema cannot be walked
back once people depend on it. The same argument says wait here: an exported Go
API cannot be walked back either, and I have far less confidence in the current
shape of `pipeline.ExecutionPlan`, `runtime.Options`, and `node.Descriptor` than
in the YAML schema.

Three specific things are likely to change, and each would be a breaking change
if exported today:

- `Descriptor` allows exactly one output. Branching needs more, and the reviews
  agreed it is coming.
- Node arity is capped at two by `NewTypedNode2`, and widening it probably means
  a different constructor shape rather than `NewTypedNode3`.
- Type compatibility is `TypeID` equality, which forbids interface-typed ports.
  Fixing that changes what a `Descriptor` means.

Publishing the contract before those settle would buy an ecosystem of nodes that
break on the next release. `internal/` is the reversible choice, and reversible
is what V0 should prefer.

## Consequences

### Positive

- The three redesigns above stay cheap: no external caller to break, no
  deprecation cycle, no compatibility shim.
- The engine's real extension guarantee is unaffected and still demonstrated: a
  node is an ordinary `node.Definition`, and the engine special-cases nothing.
  The eight built-ins were written against exactly the interface an external
  author would use.

### Negative

- The handoff's extensibility goal is met in architecture and not in
  distribution. Adding an integration means a fork or a pull request, which is a
  real limitation and should be stated in the README rather than glossed.
- Anyone embedding p6e as a library today cannot, at all.

### Risks

- "Later" can become "never". The trigger for revisiting is concrete: once
  multi-output ports, arity beyond two, and interface-typed compatibility are
  settled, the API is stable enough to publish, and that is the moment to do it
  in one deliberate commit rather than by accretion.

## Alternatives Considered

### Promote `node` and `types` only

Rejected: it exports the authoring half without the running half, so the author
still cannot execute what they wrote. It looks like progress while delivering
none, and it commits to `Descriptor`'s current single-output shape anyway.

### Promote everything and accept a v0 compatibility disclaimer

Defensible, and standard Go practice with a `v0` module version, where breaking
changes are permitted. Rejected for now on the grounds that the disclaimer is
weaker in practice than in theory: people build on v0 modules and are unhappy
when they break. Waiting costs nothing while there are no external users.

### Add a thin root facade (`p6e.Compile`, `p6e.Run`) over the internal packages

The most attractive alternative, and the likely eventual answer: it would let the
internals move freely behind a small, deliberately designed surface. Rejected as
out of scope here because designing that surface well is a task in itself, not a
side effect of a cleanup commit.

## References

- ADR 0005 for the same reversibility argument reaching the opposite conclusion,
  because a YAML schema in the wild is harder to change than an unexported Go API.
- `.claude/doc/p6e-architecture-review.md`, which recommended the promotion.
  Local only, not committed.

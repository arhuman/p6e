# 5. Named input binding

Date: 2026-08-12

## Status

Accepted, extended by ADR 0009.

Extends ADR 0002, which chose positional binding and predicted this change.

The named form described here is unchanged. ADR 0009 closes the risk left open
below by making it mandatory, as a compile error rather than the warning
suggested here, for nodes whose input ports are not pairwise type-distinct.

## Context

ADR 0002 bound `needs` to input ports by position and recorded the cost under
Risks: order is load-bearing in a list that looks like a set, and when two ports
have the same type a swap type checks and the pipeline silently does the wrong
thing. It deferred the fix on the grounds that V0 had no multi-input node where
this could bite.

A multi-model review then ranked this the first or fifth most serious issue in
the codebase depending on the reviewer, and one made the argument that decided
it: **a dangerous declarative schema cannot be walked back.** Performance can be
fixed behind an unchanged interface later; a YAML semantic cannot, once people
have pipelines written against it. The cost of this change is roughly constant
over time while the cost of deferring it grows with every pipeline written.

The concrete failure, from the review: a node taking `(UserID, UserID)` bound
`needs: [alice, bob]`, edited later to `needs: [bob, alice]`. Types match, the
compiler is satisfied, the engine runs, and the relationship comes out inverted.

## Decision

**Accept a mapping form of `needs` that binds by input port name. Keep the list
form, unchanged, as positional.**

```yaml
decode:
  uses: json.decode
  needs: [fetch]              # positional, unchanged

report:
  uses: report.render
  needs:                      # named
    left: fetch_a
    right: fetch_b
```

This is the compatible superset ADR 0002 sketched, implemented as sketched: a
list stays positional, a mapping binds by port name.

The named form is checked exhaustively at compile time. Every declared input port
must be bound, and every binding must name a real port:

```text
step "joined": input "in1" of node "join" is not bound by needs (inputs: "in0", "in1")
step "joined": needs binds "in2", but node "join" has no such input (inputs: "in0", "in1")
```

Both are compile errors, so a typo in a port name cannot leave an input silently
unconnected. Per-port type checking is unchanged: each bound edge is compared
against the type its port declares.

Internally the compiler resolves either form to dependency indices in port order
in `resolveNeeds`, and stores them on the compiler for the plan builder to reuse.
The plan format, the executor, and the node contract are untouched: they only
ever saw ordered indices, and they still do.

## Consequences

### Positive

- The swap trap is avoidable, and avoiding it is a local choice made by the
  author of the step that has the problem.
- Existing pipelines and examples continue to compile unchanged. Nothing was
  deprecated.
- The named form is self-documenting at the call site: a reader sees which
  dependency feeds which port without opening the node's source.
- Port names become part of a node's public contract, which is a good pressure:
  `in0` and `in1`, the defaults from `NewTypedNode2`, are poor names, and the
  named form makes that visible to whoever writes the pipeline.

### Negative

- Two ways to express one thing. The rule to keep it from being confusing is
  a convention rather than a constraint: use the list for one input, the mapping
  for more than one.
- `NewTypedNode2` still generates `in0` and `in1`, so the named form is currently
  more verbose than it is clearer for built-in nodes. Giving the adapter a way to
  name ports is the obvious follow-up and is not done here.

### Risks

- Nothing forces the named form where it matters most. A same-typed two-input
  node can still be bound positionally and still swapped. A compiler warning when
  a positional binding feeds two ports of identical type would close that, and is
  worth doing if such nodes become common.

  Closed by ADR 0009, which rejected the warning and made the case a compile
  error: a warning in a compile-time-first engine is a defect that ships.

## Alternatives Considered

### Make named binding mandatory for multi-input nodes

Would close the risk above completely. Rejected for now because it is a breaking
change for a shape that does not exist yet in the built-in node set, and because
the warning above achieves most of it without breaking anything.

ADR 0009 revisited this and took a narrower version: mandatory for nodes whose
input ports share a type, not for every multi-input node. The "does not exist
yet" argument turned out to favour acting rather than waiting, since the change
is free precisely while no such node exists.

### Deprecate the positional form entirely

Rejected. `needs: [fetch]` is the overwhelmingly common case and
`needs: {in: fetch}` is worse to read and write for no benefit: a single port
cannot be bound to the wrong thing.

## References

- ADR 0002, which chose positional binding and predicted this change under Risks.
- `.claude/doc/p6e-architecture-review.md`, the review that argued the timing.
  Local only, not committed.

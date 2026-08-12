# 9. Requiring named binding when input ports share a type

Date: 2026-08-12

## Status

Accepted

Extends ADR 0005, which added the named form and left this under Risks.

## Context

ADR 0005 made `needs` accept a mapping that binds by input port name, alongside
the original list that binds by position. It closed the swap trap for authors
who choose the mapping, and recorded what it did not close:

> Nothing forces the named form where it matters most. A same-typed two-input
> node can still be bound positionally and still swapped.

It proposed a compiler warning for that case and rejected making the mapping
mandatory, on the grounds that it would be a breaking change for a node shape
the built-in set does not contain.

Two things make that reasoning worth revisiting.

**A warning is the wrong instrument in this engine.** p6e's central invariant is
that anything checkable before execution is checked, and `p6e check` is
decisive: it either rejects a pipeline or proves it sound. A warning is an
admission that the compiler found a defect and shipped the pipeline anyway. Here
the compiler has everything it needs to be decisive, because ambiguity is a
property of the node descriptor alone.

**The breaking-change argument inverts with time.** It is only true that this
breaks nothing while no such node exists. That is the argument for doing it now
rather than the argument against: today the change costs nothing, and every
same-typed fan-in node written before the rule lands makes it cost more. This is
ADR 0005's own reasoning about declarative schemas, applied one step further.

The residue is a real asymmetry. Positional binding on ports of distinct types
is safe, because a swap fails the type check. Positional binding on ports of the
same type is the single construct in the whole format that the compiler cannot
judge. Those two cases deserve different treatment, and ADR 0005 gave them the
same treatment.

## Decision

**A list form of `needs` is a compile error when the target node's input ports
are not pairwise type-distinct. Those nodes must be bound by the mapping form.**

```yaml
# Rejected: both inputs of "pair" are Alpha, so either order type checks.
paired:
  uses: pair
  needs: [left, right]

# Required instead.
paired:
  uses: pair
  needs:
    in0: left
    in1: right
```

```text
step "paired": node "pair" has inputs of identical type Alpha ("in0", "in1"),
  so a positional swap would type check: bind needs by name instead
```

The rule keys on **ambiguity, not arity**. A fan-in whose ports have distinct
types stays bindable positionally, unchanged:

```yaml
joined:
  uses: join            # (Alpha, Beta) -> Beta
  needs: [source, convert]
```

That is the narrow form of the rule. The broad alternative, requiring the
mapping for every node of arity above one, was rejected: it imposes ceremony
where the type check already proves the binding correct, and buys nothing.

`Descriptor.AmbiguousInputs` reports the first duplicated input type and the
ports sharing it, scanning in port order so the message is identical on every
compile. The compiler consults it in `resolveNeeds` before the arity check,
because a node with ambiguous ports needs the mapping form whatever the list
contains, and reporting a count first would send the author back to a form they
cannot use.

## Consequences

### Positive

- The last silent-failure class in the format is unreachable rather than
  documented. Every remaining way to misbind an input is a compile error.
- The engine's claim is now literally true: if `p6e check` passes, no edge is
  bound to the wrong port.
- The cost is paid only where the danger is. Single-input steps, and fan-in
  steps with distinct types, are untouched.
- It lands while the built-in node set contains no ambiguous node, so no
  existing pipeline, example, or documented form changes.

### Negative

- Two forms of `needs` now have a rule governing which is legal, rather than
  being purely a matter of taste. The rule is mechanical and the error explains
  itself, but it is one more thing that is true about the format.
- A node author changing an input's type can invalidate pipelines that did not
  change, by making two ports collide. This is a real coupling, and it is the
  same coupling that already exists for edge types: it surfaces at compile time
  with a message naming the cause.

### Risks

- Port names become load-bearing for a node whose ports collide, and
  `NewTypedNode2` still generates `in0` and `in1`. A node with two same-typed
  ports now forces authors to write those names, which are bad names. Giving the
  adapter a way to name ports was already the obvious follow-up in ADR 0005 and
  is now more clearly worth doing.

## Alternatives Considered

### A compiler warning, as ADR 0005 proposed

Rejected. The compiler either proves the pipeline sound or it does not, and a
warning in a compile-time-first tool is a defect that ships. It also has no
delivery mechanism today: `CompileError` collects problems that stop
compilation, and adding a parallel non-fatal channel is more machinery than the
decisive rule needs.

### Require the mapping form for every node of arity above one

Gemini's position in the review. Rejected as described above: it triggers where
the type check already suffices. The duplicate-type rule triggers exactly where
the compiler loses its power and nowhere else.

### Rely on distinct types by convention

Tell node authors to wrap same-typed ports in distinct named types, the way Go
code avoids positional-argument bugs. Rejected as a rule, though it stays good
advice: it moves a guarantee into a convention that nothing enforces, and some
nodes genuinely take two of the same thing.

## References

- ADR 0002, which chose positional binding and predicted the swap trap.
- ADR 0005, which added the named form and left this rule under Risks.
- `.claude/doc/explicit-ports-yaml.md`, the multi-model evaluation that produced
  the duplicate-type formulation. Local only, not committed.

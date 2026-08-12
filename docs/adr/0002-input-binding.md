# 2. Binding a step's dependencies to its input ports

Date: 2026-08-12

## Status

Accepted

## Context

A step declares what it consumes with a `needs` list:

```yaml
notify:
  uses: slack.send
  needs:
    - format
```

A node declares typed input ports. Something has to say which dependency feeds
which port, and the pipeline format has no syntax for it. The handoff (section
18) says not to over-design interpolation yet, which leaves the question open,
and it cannot stay open: the compiler needs the answer to type-check an edge.

## Decision

**`needs` is ordered, and binds positionally to the node's declared input ports.**

The first entry feeds the first port, the second the second. A step whose node
takes one input names one dependency, which is the overwhelmingly common case
and requires no extra syntax:

```yaml
decode:
  uses: json.decode
  needs: [fetch]
```

A fan-in node takes its inputs in the order the node declares them:

```yaml
report:
  uses: report.render      # (JSONDocument, CommandResult) -> Report
  needs: [decode, probe]
```

**A count mismatch is a compile error**, reported like a type error:

```text
step "report": node "report.render" expects 2 inputs (JSONDocument, CommandResult)
  but needs lists 1
```

## Consequences

### Positive

- Nothing new in the file format. The existing `needs` list carries the binding.
- Trivial for the common single-input case, which is most steps.
- Fully checkable at compile time, alongside the type check it feeds.

### Negative

- **Order is load-bearing in a list that looks like a set.** Swapping two entries
  of a fan-in step changes meaning. When both ports have the same type the swap
  type-checks and the pipeline silently does the wrong thing. This is the real
  cost of the decision.
- **Ordering-only dependencies are not expressible.** `needs` means "consumes
  the output of", so a step cannot say "run after X" without taking X's value.
  V0 has no side-effect-ordering use case; when one appears it wants its own
  key (`after:`) rather than an overload of `needs`.

### Risks

- Same-typed fan-in ports are a silent-error trap. If that shape becomes common,
  the answer is a named form, not a warning:

  ```yaml
  needs:
    left: fetch_a
    right: fetch_b
  ```

  That is a compatible superset: a list stays positional, a mapping binds by
  port name. Deliberately not built yet, because V0 has no node that needs it.

## Alternatives Considered

### Named binding from the start (`needs: {port: step}`)

Unambiguous, and immune to the swap trap. Rejected for V0: it makes the common
single-input step wordier (`needs: {in: fetch}` rather than `needs: [fetch]`)
for a safety property that only matters for multi-input nodes, of which V0 has
none. The upgrade path above keeps it available.

### An expression syntax (`with: {body: "${{ fetch.output }}"}`)

What n8n and GitHub Actions do. Rejected outright: it is a DSL, an interpolation
evaluator, and a run-time resolution step, all of which the handoff lists as
non-goals. Expressions also move type checking to run time, which is the one
thing this engine exists to avoid.

## References

- Handoff sections 18 and 19 (configuration, DAG compilation).
- ADR 0001 for the type identity the edge check compares.

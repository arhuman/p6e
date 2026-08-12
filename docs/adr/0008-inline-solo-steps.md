# 8. Inline execution of a solitary ready step

Date: 2026-08-12

## Status

Accepted

## Context

ADR 0003 measured that about 60% of a step's cost was the goroutine handoff: the
executor spawned a goroutine per ready step and received its completion on a
channel, a round trip measuring 254ns against a step total of 433ns. It named the
fix and deferred it: run a step on the coordinator goroutine when it is the only
one ready, since the goroutine and the channel buy nothing in that case.

ADR 0004 then added a guarantee that complicates this: once the execution winds
down, `Run` returns within `AbandonAfter`, even if a node ignores its context,
because the coordinator is free to stop waiting. An inlined node is executed *by*
the coordinator, so while it runs there is nobody left to give up. Inlining and
the abandonment guarantee are in direct tension, which neither ADR anticipated.

## Decision

**Implement inlining, and make it opt-in via `Options.InlineSoloSteps`,
defaulting to off.**

When the option is set and there is exactly one ready step with nothing in
flight, the coordinator calls `runStep` directly instead of launching. Both paths
record their completion through the same `handle` closure, so scheduling,
skip propagation, retry, and mutation detection cannot drift between them.

The default is off because the two properties are not equally valuable. A
general-purpose engine that can be wedged by one badly behaved node is worse than
a slow one, and 433ns per step was never the binding constraint on anything.
Callers who know their nodes honour cancellation can have the speed by asking for
it. `p6e run --inline` exposes it, with the trade stated in the help text.

## Measurements

Same machine and toolchain as ADR 0003, medians of three runs, all figures from a
single interleaved run so they are directly comparable.

| Shape | Default | Inlined | Change |
|---|---|---|---|
| 100-step chain | 486 ns/step | 101 ns/step | **4.8x faster** |
| 5-step chain | 581 ns/step | 150 ns/step | 3.9x faster |
| 100-way fan-out | 465 ns/step | 470 ns/step | unchanged |

Allocations per run, which matter as much as the time:

| Shape | Default | Inlined |
|---|---|---|
| 100-step chain | 112 allocs, 35.8 KB | **12 allocs, 26.2 KB** |
| 5-step chain | 17 allocs | 12 allocs |

The allocation collapse is the clearest signal that the right thing was removed:
100 goroutine closures disappear from a 100-step chain, leaving only the
per-execution fixed cost. A sequential step now costs 101ns, of which the typed
adapter is 11.4ns, so the engine's remaining overhead per sequential step is
roughly 90ns of bookkeeping.

Fan-out is unchanged within noise, confirming the fast path costs nothing where
it does not apply: only the root of a fan-out is ever solitary.

## Consequences

### Positive

- Sequential pipelines, the most common shape, cost a fifth of what they did.
- No goroutine, no channel round trip, and no closure allocation for an inlined
  step.
- Both execution paths share one completion handler, so there is one scheduler to
  reason about rather than two.
- Nothing else changed: the plan format, the node contract, the YAML schema and
  the compiler are untouched. It is a pure runtime option.

### Negative

- **While an inlined node runs, `Run` cannot honour its context.** A node that
  ignores cancellation wedges the caller instead of leaking a goroutine, which is
  exactly the failure ADR 0004 removed. This is why it is off by default, and it
  should stay off for any pipeline containing third-party nodes.
- Two scheduling paths mean two things to keep in step. The shared `handle`
  closure limits that, and three tests assert the inline path produces the same
  results, still fans out, and still propagates failure.

### Risks

- The option is attractive enough that someone will turn it on globally and
  forget the trade. The help text and the field comment both state it; a
  reasonable follow-up is for `Run` to log once when inlining is enabled and a
  step exceeds `AbandonAfter`, since that is the situation in which the caller
  has silently lost their timeout.

## Alternatives Considered

### Inline by default

Rejected. It would silently undo ADR 0004 for the most common pipeline shape,
trading an availability guarantee for latency nobody had asked for.

### A reusable worker pool instead

Avoids goroutine creation while keeping the coordinator free, so it would keep
the abandonment guarantee and could be the default. It recovers less: the channel
round trip remains, which is a large part of the 254ns. It also introduces worker
lifecycle and fairness across concurrent executions, which is the scheduler
subsystem ADR 0004 declined to build. Still the right answer if the guarantee
ever needs to hold *and* sequential chains need to be fast; inlining was cheaper
to build and measure first.

### Inline with a watchdog that abandons a stuck inlined node

Not possible. Go cannot interrupt a running goroutine, and the coordinator is the
goroutine in question.

## References

- ADR 0003, which identified and quantified this optimization.
- ADR 0004, whose abandonment guarantee this trades against.

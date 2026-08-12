# 3. V0 baseline performance and where the time goes

Date: 2026-08-12

## Status

Accepted

## Context

The handoff (section 31) says not to impose numeric targets before measuring
V0. This ADR is that measurement: a baseline to defend against regressions, and
an honest account of what the engine currently spends per step, so the next
optimization is chosen from data rather than intuition.

The metric that matters is the one the handoff names: **the latency the engine
adds between node A completing and node B being invoked.** Every node body in
these benchmarks returns a preallocated pointer, so what is measured is the
runtime, not the work.

Apple M3 Pro, Go 1.26.2, darwin/arm64, `go test -bench . ./internal/runtime`.

## Measurements

| Shape | ns/step | allocs/run | Notes |
|---|---|---|---|
| 5-step chain | 626 | 16 | the handoff's source/noop/noop/noop/sink |
| 100-step chain | 517 | 111 | per-run setup amortized |
| 100-way fan-out | 534 | 112 | all steps ready at once |
| 64-leaf fan-in tree | 559 | 139 | binary merges, 127 steps |
| 5-step chain, 12 goroutines | 205 | 16 per run | one plan, concurrent executions |
| 16 MiB payload to 32 consumers | 649 | 44 | **11.9 KB allocated in total** |

Two figures for context:

- **Typed adapter, one edge: 15.3 ns, zero allocations** (ADR 0001).
- **Bare goroutine spawn plus channel round trip: 318 ns, 1 allocation.**

## What the numbers say

**The type bridge is not the cost.** It is 15 ns of the ~520 ns a step takes,
under 3%. The design question ADR 0001 agonized over turned out to be settled
correctly and cheaply; the expensive part is elsewhere.

**Sixty percent of a step is the scheduler's goroutine handoff.** The executor
spawns a goroutine per ready step and receives its completion on a channel.
That round trip alone measures 318 ns, so no step can currently cost less. The
remaining ~190 ns is the closure allocation, the state bookkeeping, and channel
contention once many steps land at once.

**Nothing copies payloads.** A 16 MiB value delivered to 32 consumers costs
11.9 KB and 44 allocations for the whole run. Fan-out shares one reference, as
designed; the allocation figure is independent of payload size, which is the
property that makes the engine usable for images and large documents.

**Compilation is 34 us for a 100-step pipeline**, paid once per plan and never
per run. Parsing, resolution, cycle detection, and type checking are all in
that figure. This is the trade the architecture is built on and it is a good one.

## Decision

**Accept these as the V0 baseline and do not optimize the scheduler yet.**

The obvious next win is visible and quantified: **when exactly one step is ready,
run it on the coordinator goroutine instead of spawning one.** A sequential
chain is the common shape, and it would drop from ~520 ns to something near the
adapter's 15 ns plus bookkeeping. A worker pool would help the fan-out case by
reusing goroutines rather than spawning per step.

Neither is built now, for the handoff's stated reason (section 21): correctness
and clean ownership first. The current executor has a single-owner design where
one goroutine owns all execution state and nothing else touches it, which is why
it is race-free by construction rather than by careful locking. Inline execution
preserves that property and can be added later without changing the plan format,
the node contract, or any test.

## Consequences

### Positive

- A regression in any of these shapes is now visible: `make bench` reports
  ns/step for each.
- The next optimization is identified, bounded, and justified by measurement
  rather than taste.
- The zero-copy claim is a test, not an assertion.

### Negative

- 520 ns per step is roughly 35 times the theoretical floor set by the adapter.
  For a pipeline of a few dozen steps that is tens of microseconds, invisible
  next to any real node's work, but it does mean p6e is not yet the right tool
  for a pipeline of thousands of trivial steps in a hot loop.

### Risks

- These numbers are from one machine. They are a relative baseline for spotting
  regressions, not an absolute promise.

## References

- Handoff sections 30 and 31 (performance philosophy, resource philosophy).
- ADR 0001 for the per-edge adapter cost this is measured against.
- `internal/runtime/bench_test.go`.

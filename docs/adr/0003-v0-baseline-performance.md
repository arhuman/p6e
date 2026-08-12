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

Apple M3 Pro, Go 1.26.5, darwin/arm64, `go test -bench . ./internal/runtime`.
Median of three runs. See Revisions for the earlier Go 1.26.2 figures.

## Measurements

| Shape | ns/step | allocs/run | Notes |
|---|---|---|---|
| 5-step chain | 520 | 16 | the handoff's source/noop/noop/noop/sink |
| 100-step chain | 433 | 111 | per-run setup amortized |
| 100-way fan-out | 446 | 112 | all steps ready at once |
| 64-leaf fan-in tree | 465 | 139 | binary merges, 127 steps |
| 5-step chain, 12 goroutines | 176 | 16 per run | one plan, concurrent executions |
| 16 MiB payload to 32 consumers | 537 | 44 | **11.9 KB allocated in total** |

Two figures for context:

- **Typed adapter, one edge: 11.4 ns, zero allocations** (ADR 0001).
- **Bare goroutine spawn plus channel round trip: 254 ns, 1 allocation.**

## What the numbers say

**The type bridge is not the cost.** It is 11 ns of the ~433 ns a step takes,
under 3%. The design question ADR 0001 agonized over turned out to be settled
correctly and cheaply; the expensive part is elsewhere.

**Sixty percent of a step is the scheduler's goroutine handoff.** The executor
spawns a goroutine per ready step and receives its completion on a channel.
That round trip alone measures 254 ns, so no step can currently cost less. The
remaining ~180 ns is the closure allocation, the state bookkeeping, and channel
contention once many steps land at once.

**Nothing copies payloads.** A 16 MiB value delivered to 32 consumers costs
11.9 KB and 44 allocations for the whole run. Fan-out shares one reference, as
designed; the allocation figure is independent of payload size, which is the
property that makes the engine usable for images and large documents.

**Compilation is 26 us for a 100-step pipeline**, paid once per plan and never
per run. Parsing, resolution, cycle detection, and type checking are all in
that figure. This is the trade the architecture is built on and it is a good one.

## Decision

**Accept these as the V0 baseline and do not optimize the scheduler yet.**

The obvious next win is visible and quantified: **when exactly one step is ready,
run it on the coordinator goroutine instead of spawning one.** A sequential
chain is the common shape, and it would drop from ~433 ns to something near the
adapter's 11 ns plus bookkeeping. A worker pool would help the fan-out case by
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

- 433 ns per step is roughly 38 times the theoretical floor set by the adapter.
  For a pipeline of a few dozen steps that is tens of microseconds, invisible
  next to any real node's work, but it does mean p6e is not yet the right tool
  for a pipeline of thousands of trivial steps in a hot loop.

### Risks

- These numbers are from one machine and one toolchain. They are a relative
  baseline for spotting regressions, not an absolute promise. The Go 1.26.2 to
  1.26.5 bump alone moved every figure by 15 to 25%, in our favour, without a
  line of our code changing.

## Revisions

**2026-08-12, after the post-review hardening (ADRs 0004 to 0009).** The table
above is the V0 baseline and is left as recorded. Six changes have landed since,
and they moved every figure, so this section is the current baseline.

Measured the same way, medians of five runs. The "before" column is the tree at
`build: pin toolchain go1.26.5`, extracted and benchmarked side by side rather
than quoted from memory, so the two columns share a machine and a session.

| Shape | Before | Now | Change |
|---|---|---|---|
| 100-step chain | 430 ns/step | 491 ns/step | +14% |
| 5-step chain | 522 | 588 | +13% |
| 64-leaf fan-in tree | 466 | 514 | +10% |
| 16 MiB payload to 32 consumers | 538 | 638 | +19% |
| 100-way fan-out | 455 | 468 | +3% |
| 5-step chain, 12 goroutines | 179 | 196 | +9% |
| Compile a 100-step pipeline | 26.9 us | 33.7 us | **+25%** |
| Goroutine spawn plus channel round trip | 258 | 268 | +4% |

Allocations per run rose by exactly one everywhere (the executor's ready queue).
Compilation rose by 100, from 313 to 413, and by 5.9 KB.

**The last row is the control.** `BenchmarkGoroutineHandoff` measures code that
has not changed, and it moved 4%. That is the machine drift and noise floor for
this session, so the fan-out figure is unchanged and everything at 9% or above is
real.

### Where the run-time regression came from

The executor's coordinator loop was a bare `c := <-done` receive. It is now a
three-way `select` over the completion channel, the caller's context, and the
abandon timer, and a select over three cases costs materially more than a
single-channel receive. The loop also gained a `pump` call, a `handle` closure
call, and a disabled-guard call per completion.

That is the price of ADR 0004's guarantee that `Run` honours its context, and it
is worth paying: 60 ns per step against a failure mode where one bad node wedges
the process with no recourse. It is recorded here rather than absorbed quietly,
because a 14% regression that nobody wrote down is indistinguishable from a bug
in six months.

### Where the compile regression came from

Named binding (ADR 0005) and the ambiguity rule (ADR 0009) both work per step at
compile time: `Needs` decoding, `resolveNeeds` per step, `AmbiguousInputs`
building a map per node, and `dependenciesResolve` walking each step's
dependencies again. 25% and 100 allocations on a cost paid once per plan, in
exchange for two classes of silent misbinding becoming impossible, is the
cheapest trade in this document.

### And the opt-in path is far ahead of both

| Shape | Original baseline | Now, default | Now, `--inline` |
|---|---|---|---|
| 100-step chain | 517 ns/step | 491 | **98** |
| 5-step chain | 626 | 588 | 151 |
| 100-way fan-out | 534 | 468 | 470 |

Allocations for the 100-step chain go from 112 per run to 12, because 100
goroutine closures disappear. So the honest summary of the whole exercise is that
the default path got 14% slower to become safe, and the opt-in path is 5x faster
than the engine ever was. See ADR 0008 for why inlining is not the default.

The engine's overhead per inlined sequential step is now 98 ns, of which the
typed adapter is 11.65 ns. The remaining 86 ns is per-step bookkeeping, and it is
the next thing to look at if this ever matters.

**2026-08-12, Go 1.26.2 to 1.26.5.** The original measurements were taken on Go
1.26.2, before `go.mod` pinned a toolchain to clear five reachable standard
library advisories. The newer toolchain is uniformly faster: the adapter went
from 15.3 to 11.4 ns per edge, a 100-step chain from 517 to 433 ns per step,
and the goroutine handoff from 318 to 254 ns. The table above is the current
baseline.

Every conclusion survived unchanged, which is the more useful observation: the
adapter is still under 3% of a step, the goroutine handoff is still about 60%,
and inline execution of a solitary ready step is still the identified next win.
A toolchain upgrade moved the absolute numbers and none of the ratios, so the
architecture is what determines the cost here, not the compiler.

## References

- Handoff sections 30 and 31 (performance philosophy, resource philosophy).
- ADR 0001 for the per-edge adapter cost this is measured against.
- `internal/runtime/bench_test.go`.

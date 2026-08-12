# 4. Bounding the executor: concurrency and abandonment

Date: 2026-08-12

## Status

Accepted

## Context

A multi-model review of V0 (recorded in `.claude/doc/p6e-architecture-review.md`)
found two unbounded resources in `internal/runtime`. Both were confirmed against
the code.

**The coordinator could never give up.** The loop was
`for inflight > 0 { c := <-done }` with no select on `ctx.Done()`. Its only exit
was every launched goroutine reporting a completion. Since Go cannot stop a
goroutine, a single node that ignored its context blocked `Run` forever. Worse,
a deadline supplied by the caller could not rescue it: the deadline fired,
`runCtx` was cancelled, well-behaved nodes returned, and `Run` went on waiting
for the one that did not. `defer cancel()` never ran either, so the context
leaked alongside both goroutines. For a library intended to become a daemon,
that is an availability bug rather than a contract note.

**Nothing capped concurrency.** `launch` spawned a goroutine per ready step with
no admission control, so a 10,000-way fan-out created 10,000 goroutines at once,
multiplied by the number of concurrent executions. ADR 0003 had already measured
the goroutine handoff as 60% of per-step cost, so the scaling behaviour was
predictable from data already in the repository. This was originally left alone
under the handoff's "do not prematurely optimize" guidance, which was the wrong
frame: it is not an optimization, it is a resource bound.

## Decision

**Cap concurrency with a ready queue, and give up on stragglers after a grace
period once the execution is winding down.**

### Concurrency

The coordinator keeps a `ready` queue of steps whose dependencies are met and
launches from it only while `inflight < MaxConcurrency`. Completions pump the
queue. This caps goroutines rather than merely capping parallelism, which a
semaphore acquired inside an already-spawned goroutine would not do.

`Options.MaxConcurrency` defaults to 256. That is high enough that realistic
pipelines never notice (the 100-way fan-out benchmark is unchanged) and low
enough that a pathological fan-out cannot exhaust the process. `MaxConcurrency:
1` serializes execution, which is useful for debugging.

The queue lives in the coordinator, so the single-owner property is preserved:
no locks, no atomics, no shared mutable scheduling state.

### Abandonment

`windDown` is called when the execution stops, whether because a step failed or
because the caller's context ended. It cancels `runCtx` and arms a timer of
`Options.AbandonAfter`, defaulting to 5 seconds. If the timer fires before every
step reports, the still-running steps are marked cancelled, counted in
`Execution.Abandoned`, and their goroutines are left behind.

The timer is armed only on wind-down. A healthy pipeline whose steps take longer
than `AbandonAfter` runs to completion, because from outside the engine a slow
step and a stuck one are indistinguishable, and cutting short the slow one would
be worse than waiting.

This yields three stated guarantees, now documented on `Run`:

- Once `ctx` is done, `Run` returns within `AbandonAfter`.
- Once a step has failed, `Run` returns within `AbandonAfter`.
- Otherwise `Run` waits.

## Consequences

### Positive

- A caller's context now actually bounds `Run`, which is what any Go caller
  reasonably assumes.
- One misbehaving node can no longer wedge the process. It costs a leaked
  goroutine, which `Execution.Abandoned` surfaces rather than hides.
- Goroutine count is bounded by `MaxConcurrency` regardless of graph width or
  how many executions run at once.
- Benchmarks are unchanged: 442ns per step sequential and 441ns fan-out against
  433 and 446 before, within noise, at the cost of one extra allocation per run
  for the ready queue.

### Negative

- A step still running `AbandonAfter` after a sibling failed now loses its
  result, where previously it was awaited and recorded. That is the right trade
  once the execution has already failed, but it is a behaviour change.
- Abandoned goroutines still hold their inputs alive, so a leak retains memory
  as well as a stack. Unavoidable without process isolation, which V0 excludes.
- `MaxConcurrency` defaulting to 256 is a guess, not a measurement. A pipeline
  of 500 independent slow IO steps is now slower than before by design.

### Risks

- `AbandonAfter` at 5 seconds is likewise a guess. Too low and a legitimately
  slow step is dropped after a sibling fails; too high and a wedged pipeline
  takes longer to report. Both are configurable, and `Execution.Abandoned` gives
  operators the signal they need to tune it.

## Alternatives Considered

### A semaphore inside the step goroutine

Simpler to write, and suggested by one reviewer. Rejected because acquiring the
semaphore after `go` still creates a goroutine per ready step: it bounds
concurrent node execution but not goroutine count, which was the actual problem.

### A worker pool

Rejected for V0. It would reuse goroutines and remove the spawn cost that ADR
0003 identified, but it introduces queue ownership, worker lifecycle, and
fairness across concurrent executions. The ready queue delivers the bound now;
the pool remains available later as a performance change behind an unchanged
interface.

### Selecting on `runCtx.Done()` instead of the caller's context

Rejected: `runCtx` is cancelled by the engine itself on the first failure, so
the coordinator would abandon in-flight steps immediately rather than after a
grace period, discarding results that were about to arrive.

## References

- `.claude/doc/p6e-architecture-review.md`, the multi-model review that found
  both bounds. Local only, not committed.
- ADR 0003 for the goroutine handoff measurement that predicted the scaling
  problem.

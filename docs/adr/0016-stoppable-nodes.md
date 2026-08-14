# 16. A node may declare that it honours cancellation

Date: 2026-08-14

## Status

Accepted

## Context

ADR 0004 established that Go cannot stop a goroutine, so the executor abandons a
step that outlives its context: it reports the step cancelled, counts it, leaves
the goroutine behind, and the daemon quarantines a pipeline that does it three
times running. ADR 0008 followed from the same fact: `--inline` is off by
default, because an inlined step runs on the coordinator goroutine and a node
that ignores its context wedges `Run` instead of leaking.

That reasoning is correct and it is applied uniformly, which is where it stops
being right. The engine treats every node as unstoppable, and some are not.
`exec` is the clearest case: it waits on an external process, which makes it the
likeliest node to outlive its context, and also, unlike an arbitrary Go
function, genuinely killable. `internal/nodes/exec/exec.go` already does the
killing, through `exec.CommandContext` plus a `WaitDelay` that bounds the wait
for a backgrounded grandchild holding the output pipe.

So the engine pays three times for a limitation that does not apply to every
node: `--inline` is off by default and costs roughly half the per-step overhead
ADR 0003 measured, quarantine exists to contain the leak, and the
abandoned-run metric exists to alert on it. Nothing lets a node say the
limitation is not its own.

## Decision

`node.Stoppable` is an optional interface a node may implement, through
`node.AsStoppable`, to promise that `Execute` returns promptly once its context
is done. `exec` implements it. Everything else does not, and a node that says
nothing is treated as unstoppable, which is the safe reading and the existing
behaviour.

**The claim is a promise, not a proof, and nothing relies on it for safety.**
Go cannot verify it. The abandonment deadline applies to a stoppable node
exactly as it does to any other, and `Run`'s timing guarantees are unchanged:
once the context is done or a step has failed, `Run` still returns within
`AbandonAfter`.

What the claim buys is a diagnostic. A step abandoned when its node declared
itself stoppable reports `broken_cancellation` with `KindInternal`, saying the
promise is wrong; one whose node promised nothing reports `abandoned` with
`KindCancelled`, exactly as before. That distinction is worth having because the
two mean opposite things to whoever is holding the incident: the first is a bug
in a specific node, the second is the documented cost of ADR 0004.

### What this deliberately does not do

The obvious payoff, and the reason the capability was proposed, is enabling
`--inline` by default for a plan whose nodes are all stoppable. That is **not**
done here, and the reason is the one above: the claim is unverifiable. Turning
it into a safety decision would mean the engine's guarantee against wedging
rests on a node's self-declaration, so a single node that declares wrongly
converts a leaked goroutine into a hung coordinator. ADR 0004 and ADR 0008 chose
the leak deliberately, and trading that for a performance win is a decision
about the engine's contract rather than an optimisation.

The pieces are now in place for it, which is what makes it a decision someone
can take on evidence later rather than a rewrite. What it needs first: a way to
hold a node to its promise, such as tracking `broken_cancellation` per node
across runs and refusing to inline one that has ever broken it.

## Consequences

The node contract gains an optional interface, which is a widening of the
surface ADR 0007 keeps internal. It is optional and additive, so no existing
node changes, and `AsStoppable` wraps rather than requiring implementers to know
the method name.

`AsStoppable` adds one interface indirection to `Execute` for the nodes that use
it. Measured on the executor benchmarks: no significant change to per-step time,
and `B/op` and `allocs/op` identical. Only `exec` uses it today, and `exec` runs
a process, so the indirection is invisible against what it is wrapping.

The diagnostic is only as good as the honesty of the declaration, and the one
node that declares it is the one whose kill path is written down and tested. A
node author who wraps a node that blocks on an uncancellable library call has
made the reporting worse rather than better, which is why the interface's
documentation says what it promises before it says how to use it.

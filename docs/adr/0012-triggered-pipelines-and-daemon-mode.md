# 12. Triggered pipelines and daemon mode

Date: 2026-08-13

## Status

Accepted

## Context

Until now p6e ran one pipeline and exited. That is enough for cron and CI, where
the exit code is the whole interface, but it leaves out the two things a
pipeline engine is most often asked for: run this when a webhook arrives, and
run this every five minutes.

The obvious shape is a trigger node: a step at the head of the graph that
produces an event. It does not work, and it is worth being precise about why,
because four independent problems all point the same way.

A `RuntimeNode` is pulled. The executor invokes `Execute` once its dependencies
are met, it returns one value, and the run ends. A trigger is pushed: the world
invokes it, an unbounded number of times, for as long as the process lives, and
it may own a resource that other pipelines need too. A trigger node would have
to block inside `Execute` waiting for an event, which means:

1. it holds a concurrency slot and an `ExecutionContext` while idle, so an idle
   daemon is indistinguishable from a saturated one;
2. it yields one event per `Run`, so a second event arriving during a run has
   nowhere to go;
3. `AbandonAfter` (ADR 0004) exists precisely because a node that ignores its
   context cannot be stopped, and a trigger blocking for hours looks exactly
   like a wedged node;
4. `InlineSoloSteps` (ADR 0008) would run it on the coordinator goroutine, which
   is a deadlock nobody would think to test for.

What made the answer easy was a feature added for an unrelated reason. A
pipeline can now declare typed values the run supplies (`inputs:`,
`runtime.Options.Inputs`), and the compiler type checks every consumer of them.
That is already an injection path into the graph, and it is already proven.

## Decision

**A trigger is not a node and not a step. It is a source of runs that supplies
the pipeline's declared inputs.**

```yaml
version: 1
inputs:
  body: Bytes
trigger:
  uses: trigger.webhook
  with: {path: /hooks/deploy, method: POST}
  timeout: 30s
  respond_with: reply
steps:
  ...
```

The compiler proves the trigger supplies every declared input at the declared
type. Since it had already proven every step consumes those inputs at those
types, the proof now reaches all the way out to the event: nothing about an
event's shape is discovered on the first request.

Four consequences follow, and they are the reason this shape was chosen over a
synthetic root step or a blocking node.

**The executor did not change at all.** A trigger fills `Options.Inputs` and the
ordinary run happens. There is no seeding mechanism, no lifecycle state machine,
and no trigger-shaped special case anywhere below `internal/daemon`.

**A triggered pipeline still runs by hand.** `p6e run --input body=@event.json`
fires one, offline, with no daemon and no traffic. No separate testing facility
was needed, because supplying inputs is what the daemon does too.

**Exactly one trigger per pipeline.** The type system is nominal, with no union
type and no `Any` (deliberately, per the README). Two triggers feeding one graph
would mean a downstream step accepting either payload type, which cannot be
expressed. Two triggers means two files.

**The trigger contract splits in two, by who owns the resource.** A schedule
runs its own timer (`SelfDriven`). A webhook cannot own its socket, because one
listener serves every webhook pipeline in the process and the daemon routes by
claim (`HTTPDriven`). Giving the webhook a `Listen` method would have hidden
that, and the first pipeline to bind the port would have locked out the rest.

### What the daemon adds, and only the daemon

`internal/daemon` is the one place that knows a process can outlive a run.

**Partial failure is asymmetric, deliberately.** A file that does not compile is
logged and skipped, and the daemon still starts: one typo must not stop every
unrelated webhook from answering. A route claimed by two pipelines rejects
*both*, and the daemon still starts. Serving whichever sorted first would mean
one pipeline quietly answering requests meant for its neighbour, and the only
symptom would be the neighbour never running, with nothing anywhere saying why.
Refusing both turns a silent hijack into a loud failure naming both files.

`p6e check --dir` runs the same cross-plan validation and fails on any problem,
because a collision that is only discoverable by deploying cannot be gated in
CI.

**Responses are synchronous.** Answering with an identifier and letting the
caller collect the result later needs somewhere to keep executions, which is a
persistence layer, an explicit non-goal. Waiting costs only the caller's
patience, which the pipeline's own `timeout` bounds. `respond_with` names the
step whose output becomes the body, and that output must be `Bytes` or `Text`:
turning a structure into bytes is a step's job, which is what keeps
`encoding/json` out of the engine. The trigger declares which types it can
write, so the compiler checks this without knowing any domain type.

**Overlap has a default per kind of trigger, not per trigger.** A trigger that
answers a caller defaults to `allow`, because that caller is already waiting on
its own event. Everything else defaults to `drop`, the cron convention and the
only default that cannot pile runs up faster than they finish. The kind is a
fact the trigger reports by the interface it implements; the policy is the
engine's reading of it, and `on_overlap` overrides it. There is no queue: a
queue converts a fast rejection into a slow timeout, and an unbounded one is how
a daemon dies slowly.

**Concurrency is bounded across the process.** `MaxConcurrency` bounds one run,
so forty loaded plans each entitled to 256 steps is not a bound at all.
`Options.Slots` is a semaphore shared by every run, counting *steps* rather than
pipelines, because steps are what cost goroutines. Claiming a slot is an arm of
the executor's main `select` rather than a blocking take, which is what
preserves ADR 0004's guarantee: a run whose caller gave up never waits on a slot
another pipeline holds.

**Repeated abandonment quarantines a pipeline.** ADR 0004 accepted leaking the
goroutine of a step that ignores its context, on the grounds that the process
exits shortly afterwards. In a daemon it does not. Three consecutive runs that
abandon a step take the pipeline out of service, loudly, rather than letting one
bad node degrade the process at whatever rate its trigger fires.

## Consequences

### Positive

- The engine is untouched apart from one option. Everything a long-lived process
  needs lives in one package that the CLI path never loads.
- A triggered pipeline is testable offline with the flag that already existed.
- Every trigger question that could be answered at compile time is: unknown
  trigger, bad configuration, an input the trigger does not supply, an input at
  the wrong type, a missing timeout, a `respond_with` naming a step that cannot
  answer.
- `p6e check --dir` makes route collisions a CI failure rather than a discovery.

### Negative

- **The pipeline package now depends on the trigger package**, because a plan
  holds a built trigger. That is a new edge in the dependency graph, taken so
  that `p6e check` validates a trigger rather than deferring it to load time.
- **`Compile` changed signature**, from a node registry to a `Registries` pair.
  Mechanical, but it touched every call site.
- **Capturing the slot pool costs 16 bytes per step** on the asynchronous path,
  because the per-step goroutine closure grows into the next size class.
  Allocation count and time are unchanged (measured: 493.8 against 485.4 ns per
  step, within noise). Releasing from the coordinator instead would have been
  free and wrong: an abandoned step is still running, and handing its slot back
  would let a wedged node raise the ceiling for everyone else.
- **No cron syntax.** `every: 30s` only. Cron means a parser and a timezone
  database, and this module's only dependency is a YAML library.

### Deferred: persistence

Persistence is **deferred, not refused**. Every "no" above rests on the same
fact, that the daemon keeps nothing after a run ends, and that fact is what
makes this decision small enough to be confident in. It is not a claim that
storing executions would be wrong.

Three wanted features all reduce to it, and none can be built without it:

- **Asynchronous replies.** Answering `202` with an identifier requires somewhere
  to collect the result from later. This is the one users will ask for first,
  because a webhook doing slow work should not hold a connection open.
- **Surviving a restart with work in flight.** Today a drain finishes what it can
  and the rest is lost. Redelivery needs a durable record of what was accepted
  and what completed.
- **Run history.** `Execution.Steps` is logged and dropped on purpose, since a
  daemon that accumulated executions would grow without bound. Keeping them
  needs somewhere bounded to keep them.

If it is taken on, it should be one decision covering all three rather than
three separate stores, and it wants its own ADR. Two things here are shaped to
make that possible: `trigger.Outcome` already separates what a caller is told
from how a run went, and the response contract already names a step rather than
serialising an execution, so neither has to change to add a stored one alongside.

The order matters. Adding persistence first and triggers second would have meant
designing a store before knowing what it needed to hold.

### Risks

- Quarantine is per process and clears only on restart. If a pipeline is
  quarantined for a transient reason, an operator has to notice the log and
  restart. A threshold that decays over time would be kinder and is not built,
  because a node ignoring its context is not usually transient.
- Synchronous responses tie a caller's patience to a run's duration, bounded
  only by the pipeline's own timeout. This is the constraint most likely to
  force the persistence decision above.

## Alternatives Considered

### A trigger node that blocks in `Execute`

Rejected for the four reasons in Context. It is the shape everyone reaches for
first, and every one of its problems is in the engine's most load-bearing code.

### A synthetic root step the compiler injects

Proposed in review: a top-level `trigger:` block compiled into step 0, with the
existing roots rewired to depend on it. Rejected on a concrete defect: it makes
every constant and every `env.get` wait on the event, changes what `Roots`
means, and shifts every step index. Constants are legitimate roots.

### Raw bytes as the event payload, typed by a downstream node

Rejected. It gives up the compile-time typing that motivates the whole engine,
and makes request metadata unreachable without decoding. The reason to have a
type system is to use it at the boundary, which is exactly where events arrive.

### Reusing `HTTPRequest` for the inbound request

Tempting, since the type exists. Rejected: `HTTPRequest` describes a call to
make, and an inbound request is not one. Sharing the type would let a pipeline
forward whatever arrived straight back out, which is the confusion a nominal
type system exists to prevent. The webhook supplies `body`, `method`, `path` and
`query` instead.

### Bounding concurrent executions rather than concurrent steps

Simpler, and needs no engine change: the daemon holds its own semaphore and
lowers each run's `MaxConcurrency`. Rejected because the product of two caps
either wastes capacity when pipelines are idle or fails to bound it when they
are not. Steps are the thing that costs a goroutine, so steps are what is
counted.

### Rejecting the whole daemon on a route collision

Rejected as inconsistent with skipping a file that does not compile, and worse
in practice: one bad deploy would take down every unrelated pipeline. Rejecting
both claimants prevents the hijack without the outage.

## References

- ADR 0004 for the abandonment allowance that quarantine now bounds.
- ADR 0008 for the inline fast path a blocking trigger would have deadlocked.
- ADR 0011 for the `inputs` mechanism this builds on, which is what made a
  trigger an input provider rather than a node.
- ADR 0010 for the standing refusal of an expression language, which
  `respond_with` follows by naming a step rather than an expression.
- `.claude/doc/daemon-trigger-nodes.md`, the multi-model evaluation behind this
  decision. Local only, not committed.

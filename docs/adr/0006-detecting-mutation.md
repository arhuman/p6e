# 6. Detecting immutability violations instead of preventing them

Date: 2026-08-12

## Status

Accepted

## Context

ADR 0001 chose pointer payloads on edges for performance and recorded, under
Negative, that this makes immutability "a convention rather than a guarantee".
A multi-model review made that the one finding all four reviewers reached
independently, and it is the most dangerous property of the design for two
reasons.

**Fan-out corruption.** Every dependent receives the identical pointer. A
consumer that writes through it corrupts its siblings' input. The symptom is
nondeterministic, depends on scheduling, appears in an unrelated step, and is
invisible to the race detector in every interleaving where the DAG happens to
order the accesses anyway.

**Retry aliasing**, which ADR 0001 did not follow through to. `runStep` passes
the same input slice to every attempt. A node that partially mutates its input
before failing retries against its own corrupted data, so `Retryable` is only
sound for nodes that obey a convention nothing enforces. The retry feature and
the immutability convention are coupled, and neither document said so.

## The prevention option, and why it was rejected

The obvious fix is to make the domain types in `internal/nodes/types` carry
unexported fields with constructors and accessors, so mutation is impossible
outside the owning package. It was measured before being rejected: 156 field
accesses across the node packages would have to change.

It was rejected because it buys the wrong half. Unexported fields stop
`resp.Status = 500`. They do not stop `body[0] = 'X'`, because an accessor
returning a `[]byte` returns an aliasing slice header, and making it safe means
copying, which is precisely what pointer payloads exist to avoid. The types
where mutation is most likely and most damaging are exactly the ones holding
byte slices: `Bytes`, `Response.Body`, `CommandResult.Stdout`. So the refactor
would touch 156 sites, cost a churn of the whole node API, and leave the
dangerous half of the hole open.

Go does not offer deep immutability. Pretending otherwise with a partial barrier
would be worse than being honest about the convention.

## Decision

**Leave the convention in place and make violating it detectable.**

`Options.DetectMutation` turns on a guard that renders every step's output when
it is produced, renders it again at the end of the execution, and reports any
that differ. Violations land in `Execution.Mutations` and each names the
producer whose value changed and the consumers that could have done it. The CLI
exposes it as `p6e run --detect-mutation`.

Rendering is `fmt.Sprintf("%#v", ...)`, which reaches byte slice contents and
unexported fields. It is not a hash: the two renderings are kept so a report can
show what actually changed.

A detected violation does not mark the execution failed. The run may well have
produced the right answer this time, which is what makes the bug so hard to find
without help. The CLI does exit non-zero, on the grounds that a pipeline which
got the right answer by breaking a rule has not passed.

The guard is off by default and costs nothing when off. It is a debugging
facility, not a safety net: it holds two full renderings of every payload for the
length of the run, so it is unusable on the large payloads the design otherwise
handles well.

Two regression tests pin the hazards it exists for. One mutates a fanned-out
payload and asserts the detector catches it. One asserts that a retried node
sees its own mutation from the previous attempt, documenting the aliasing as
tested behaviour rather than a footnote.

## Consequences

### Positive

- The rule becomes testable. A node author can run their pipeline once with the
  flag and know whether they broke it, instead of finding out from corrupted
  output weeks later.
- Both hazards are covered by one mechanism, including the retry aliasing that
  no amount of type discipline in the types package would have caught.
- Zero cost and zero API churn when off. The node contract, the plan format and
  the 156 call sites are untouched.
- The retry aliasing is now written down, in ADR 0001's addendum and in a test.

### Negative

- Detection is not prevention. A violation in production is still silent unless
  someone thought to run with the flag.
- The guard only catches mutations that persist to the end of the run. A node
  that mutates a value and restores it, or two consumers whose mutations cancel
  out, pass unnoticed.
- `%#v` is slow and unbounded in output size, so the facility is unusable on
  multi-megabyte payloads, which are the case where sharing matters most.

### Risks

- The guard reports the victim, not the culprit: it names the producer whose
  output changed and lists the consumers, one of which is responsible. With one
  consumer that is exact; with a wide fan-out it narrows rather than pinpoints.
  Running with `MaxConcurrency: 1` and bisecting the consumer list is the
  workaround.

## Alternatives Considered

### Unexported fields with constructors

Rejected above: 156 call sites, closes the less dangerous half.

### Copy-on-fan-out

Guarantees safety, and destroys the property ADR 0003 measured: a 16 MiB payload
to 32 consumers would go from 11.9 KB of allocation to 512 MB. Not viable.

### A `Clone()` method on the node contract, cloning inputs per retry attempt

Would fix retry aliasing specifically, at the cost of an allocation per attempt
and a method every node author must implement correctly. Rejected: it addresses
one of the two hazards and taxes the common path to do it. If retry aliasing
alone becomes a recurring problem, an opt-in `retry: {fresh_input: true}` per
step is a better shape.

### A linter forbidding assignment through an input parameter

Attractive and complementary rather than alternative. Not built here because a
correct one is a real static analysis (aliasing through locals, slices, and
function calls) and the dynamic check subsumes the easy cases.

## References

- ADR 0001 for the pointer payload decision and its addendum on retry.
- ADR 0003 for the fan-out allocation figures that rule out copying.
- `.claude/doc/p6e-architecture-review.md`, the review where all four reviewers
  independently raised this. Local only, not committed.

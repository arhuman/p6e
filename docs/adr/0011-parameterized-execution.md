# 11. Pipelines take arguments

Date: 2026-08-13

## Status

Accepted

## Context

Until now every root of a plan was a source whose value was fixed in a `with`
block, so a pipeline was a constant. The same logic against two repositories,
two customers or two environments meant two files that differed by a string.
Nothing could hand a running plan a value.

This surfaced while reviewing what triggers p6e is missing. The first answer,
that a schedule is `cron` plus the binary and a webhook is a different product,
was half right and hid the more useful finding: **the blocker for any
event-driven mode is not the HTTP server, it is that a plan has no parameters.**
A webhook has to inject a request body into the graph, and there was no
mechanism to inject anything.

Separating the two showed that the parameter half is worth building on its own,
independently of any trigger. It turns a pipeline from a script into a function,
which is useful from the command line today, and it is the prerequisite for a
server later rather than a part of one.

The runtime was already prepared for the rest. `ExecutionPlan` is immutable and
documented as safe to run many times concurrently, and
`BenchmarkConcurrentExecutions` exists to prove it. Only the missing parameter
mechanism stood between that and serving many runs from one compiled plan.

## Decision

**A pipeline declares typed inputs, and a run supplies them.**

```yaml
version: 1

inputs:
  owner: Text
  repo: Text

steps:
  url:
    uses: text.format
    with:
      template: "https://api.github.com/repos/{{owner}}/{{repo}}"
    needs:
      owner: owner
      repo: repo
```

```bash
p6e run pipeline.yaml --input owner=golang --input repo=go
p6e run pipeline.yaml --input payload=@body.json
```

Three choices make this fit the existing engine rather than sit beside it.

### An input is a graph node, not a separate concept

An input compiles to a `CompiledStep` with no node, occupying the leading
positions of `ExecutionPlan.Steps`. Everything downstream is therefore unchanged:
`needs` binds an input exactly as it binds a step, dependencies are still
indices, `Dependents` and the input buffer are built the same way, and the
executor gathers a step's inputs without knowing which of them were supplied.

An input is not a root. It carries no computation, so the executor records its
value before scheduling anything rather than putting it in the ready queue.

### Inputs and steps share one namespace

`needs: [payload]` cannot say whether it meant an input or a step, so a name
used by both is a parse error. The alternative, a qualified form such as
`needs: [inputs.payload]`, was rejected: it is more to write for every reference
in exchange for allowing a collision that has no reason to exist.

### The type check spans compile time and run time

The compiler verifies that each declared type is registered, and type checks
every edge leaving an input exactly as it checks an edge leaving a step. What it
cannot know is the value, so `Run` checks that each supplied value carries the
declared type. That run-time check is what makes the compiler's proof hold for
values it never saw, and it is the only new run-time check in the engine.

A missing or ill-typed input fails that input's step, which stops the run before
any node executes and leaves everything downstream skipped. Routing it through
the ordinary failure path means it needs no new reporting: it reads like any
other failed step.

## Consequences

### Positive

- One compiled plan serves many runs. A pipeline is a function of its inputs.
- `p6e check` still needs no values, so a pipeline whose inputs are secrets
  validates on a machine that does not hold them.
- Nothing about the existing format changed. A pipeline without an `inputs`
  block compiles to exactly the plan it did before, with no inputs, no extra
  steps and the same roots.
- The prerequisite for an event-driven mode now exists. A server would supply
  `Options.Inputs` per request against one shared plan; nothing else is missing
  from the runtime.

### Negative

- **The compile-time guarantee no longer covers everything a run needs.** A
  pipeline can pass `p6e check` and still fail immediately because an input was
  not supplied. This is unavoidable for a parameter, and it is bounded: the
  failure is before any node runs, names the input, and cannot be a type error.
- `ExecutionPlan.Steps` now mixes inputs and steps, so anything walking it must
  use `IsInput` to tell them apart. The CLI's step count subtracts them.
- Two ways to get a value from outside: `env.get` and an input. They differ in
  who supplies it, and the guidance is that an input is for what varies per run
  while `env.get` is for what varies per environment.

### Risks

- Inputs are all required. A default would make one optional, and the three
  nodes that already carry a `default` show the pattern is wanted. It is left
  out until something needs it, since adding one later is compatible while
  removing it would not be.

## Alternatives Considered

### A separate `Inputs` array outside `Steps`

Rejected. It would force every consumer of a dependency index to ask which of
two arrays it addressed, including the executor's hot path, in exchange for a
tidier-looking plan struct.

### Injecting values through `ExecutionContext`

Rejected. It would put a map of workflow data into the type every node receives,
which is the opposite of the node contract's direction: values reach a node
through typed edges, and `ExecutionContext` holds execution identity only.

### `p6e run --set name=value` interpolated into `with` blocks

Rejected for the reason ADR 0010 rejects an expression language. Substituting
text into configuration before parsing means the thing that compiles is not the
thing that was written, and the type check happens after the substitution rather
than over the graph.

## References

- ADR 0010 for the rule that values arrive on edges rather than through syntax.
- `.claude/doc/useful-pipeline-nodes.md`, whose revised triggers section
  separated parameterized execution from server mode. Local only, not committed.

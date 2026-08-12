# The p6e pipeline file format

A pipeline is a YAML document describing a directed acyclic graph of steps. It
is compiled into an `ExecutionPlan` and then run. Everything checkable before
execution is checked at compile time, so `p6e check` either rejects the file or
proves that every node exists, every dependency resolves, the graph is acyclic,
every edge carries the type its consuming port expects, and every `with` block
is valid for its node.

This document is the reference for the format and for every node that ships
with V0.

## Contents

- [Document structure](#document-structure)
- [Steps](#steps)
- [`uses`](#uses)
- [`needs`](#needs)
- [`with`](#with)
- [`retry`](#retry)
- [Types on edges](#types-on-edges)
- [Node catalogue](#node-catalogue)
- [Errors](#errors)
- [Execution semantics](#execution-semantics)
- [Compile error reference](#compile-error-reference)
- [CLI](#cli)

## Document structure

```yaml
version: 1

steps:
  <step-id>:
    uses: <capability>
    needs: <list or mapping>     # optional
    with: <node configuration>   # optional
    retry:                       # optional
      max_attempts: <int>
      backoff: <duration>
```

`version` and `steps` are the only top-level keys.

| Key | Required | Value |
|---|---|---|
| `version` | yes | Must be `1`. It is the only schema version this build understands. |
| `steps` | yes | A mapping of step ID to step. Must contain at least one entry. |

Two document-level rules:

- **Decoding is strict.** An unknown field anywhere is an error, not something
  ignored. A typo that is silently dropped produces a pipeline that checks clean
  and then does something other than what it says.
- **A document is capped at 1 MiB** (`MaxPipelineBytes`). Input over the limit
  is an error rather than a truncation, because a silently truncated pipeline
  would compile into a different, smaller graph.

## Steps

A step is one node invocation. The mapping key is the step ID, used in `needs`,
in error messages, and in run output.

```yaml
steps:
  fetch:            # <- the step ID
    uses: http.request
    needs: [request]
```

Step IDs are ordinary YAML mapping keys, with no character restrictions imposed
by p6e. A step may not name itself in `needs`.

Order in the file is irrelevant. YAML mappings have no order once decoded, so
steps are sorted by ID before compilation. That is what makes a plan
deterministic: the same file produces the same plan, the same step order in
output, and the same first error, every time.

## `uses`

Required. Names a capability in the node registry, for example `http.request`.
`p6e nodes` lists what is available. An unknown name is a compile error.

The capability is resolved and the node is **constructed at compile time**, once
per step. That is what allows a node to validate its configuration once, before
anything runs, and it is why a node's descriptor may depend on its
configuration: the `value` node's output type is whatever its `with.type` says.

## `needs`

Optional. Declares which steps feed this step's input ports. A step with no
`needs` is a root and starts immediately.

Two forms are accepted.

### Positional (a list)

Binds in port order: the first entry feeds the first declared input port, the
second the second.

```yaml
decode:
  uses: json.decode
  needs: [fetch]
```

This is the idiom for single-input steps, which is nearly every step, and it is
what all four bundled examples use.

### Named (a mapping)

Binds by input port name.

```yaml
report:
  uses: report.render     # (before Snapshot, after Snapshot) -> Report
  needs:
    before: snapshot_v1
    after: snapshot_v2
```

The named form is checked exhaustively: every declared port must be bound, and
every binding must name a real port. A typo cannot leave an input silently
unconnected. Writing the mapping keys in a different order does not change the
binding.

### Which form is required

The mapping form is **mandatory when the node's input ports are not pairwise
type-distinct**, and optional everywhere else.

That rule exists because two ports of the same type are the one binding mistake
the type check cannot catch: both orders of a list type check, and the pipeline
silently does the wrong thing. Where port types differ, a swap fails the type
check on its own, so the list stays legal.

```yaml
# Rejected: both of "pair"'s inputs are Alpha, so either order type checks.
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

**No built-in node has more than one input**, so this rule never fires against
the bundled catalogue. It applies to nodes you add. See ADR 0002, ADR 0005 and
ADR 0009.

### Port names

Port names come from the node's descriptor, not from the pipeline. The adapters
in `internal/node` generate them:

| Adapter | Input ports | Output port |
|---|---|---|
| `NewSource` | none | `out` |
| `NewTypedNode` | `in` | `out` |
| `NewTypedNode2` | `in0`, `in1` | `out` |

So the named form of a single-input built-in is `needs: {in: fetch}`. It is
legal and equivalent to `needs: [fetch]`, and it is not worth writing: a single
port cannot be bound to the wrong thing.

### What `needs` does not do

`needs` means "consumes the output of". A step cannot say "run after X" without
also taking X's value. V0 has no ordering-only dependency; when one is needed it
gets its own key rather than an overload of `needs`.

There is also no port selector, because a node has exactly one output. A step
name in `needs` always refers to that whole output.

## `with`

Optional. The node's configuration. Its shape is defined entirely by the node,
which decodes it once, at compile time. Unknown fields are rejected.

Nodes that take no configuration (`json.decode`, `exec`, `http.body`) reject a
`with` block rather than ignoring it, so a misplaced key fails at check time.

The `with` block is static. There is no interpolation, no expression language,
and no `${{ steps.fetch.output }}`: data reaches a node through edges, and
anything else would move type checking to run time, which is the thing this
engine exists to avoid.

## `retry`

Optional. The workflow's retry policy for one step.

```yaml
fetch:
  uses: http.request
  needs: [request]
  retry:
    max_attempts: 3
    backoff: 200ms
```

| Field | Type | Default | Rules |
|---|---|---|---|
| `max_attempts` | int | `1` | Counts the first try, so `1` means no retry. Must be between 1 and 100. |
| `backoff` | duration string | `0` | Delay before the second attempt, doubling for each attempt after. `"250ms"`, `"2s"`. Must not be negative. |

A step that declares no `retry` gets `max_attempts: 1`: it is tried once.

With `max_attempts: 4` and `backoff: 100ms`, the delays are 100ms, 200ms, 400ms.
A zero or absent backoff retries immediately.

**Retry is policy, not node behaviour.** A node reports facts (an error's `Kind`
and its `Retryable` flag); the workflow declares intent; the engine applies it.
A step is retried only when **all** of these hold:

1. the node returned an error,
2. that error is `Retryable`,
3. the attempt count has not reached `max_attempts`.

`Retryable` defaults to true only for `transient` errors. A `permanent` error is
never retried no matter what the policy says, so raising `max_attempts` on a
step failing with a 404 changes nothing.

The upper bound of 100 exists because an unbounded attempt count with a doubling
backoff occupies a concurrency slot for hours, and nothing legitimate wants it: a
step that fails that many times needs a person, not another attempt.

## Types on edges

An edge carries a typed Go pointer. Nothing is serialized between native nodes.
Types are compared **nominally**: two types are compatible if and only if their
type names are equal. There is no subtyping and no implicit conversion, so
turning a `HTTPResponse` into `Bytes` takes a step (`http.body`) that says so.

| Type name | Go type | Shape |
|---|---|---|
| `Bytes` | `*types.Bytes` | `Value []byte` |
| `Text` | `*types.Text` | `Value string` |
| `Bool` | `*types.Bool` | `Value bool` |
| `Int` | `*types.Int` | `Value int64` |
| `JSONDocument` | `*types.Document` | `Root any`, holding `map[string]any`, `[]any`, or a scalar |
| `HTTPRequest` | `*types.Request` | `Method string`, `URL string`, `Headers map[string]string`, `Body []byte` |
| `HTTPResponse` | `*types.Response` | `Status int`, `Headers http.Header`, `Body []byte` |
| `Command` | `*types.Command` | `Name string`, `Args []string`, `Dir string`, `Timeout time.Duration` |
| `CommandResult` | `*types.CommandResult` | `ExitCode int`, `Stdout []byte`, `Stderr []byte` |

Note that three type names differ from their Go type names: `JSONDocument`,
`HTTPRequest` and `HTTPResponse`. The name in the table is what appears in
pipeline error messages.

**Values are immutable.** Fan-out hands every dependent the same pointer, and
nothing copies a payload, so mutating a value in place corrupts a sibling's
input. Retry has the same exposure, because every attempt receives the same
input. Go cannot enforce this, so it is checkable instead: `p6e run
--detect-mutation` reports any node that wrote through a value it does not own
(ADR 0006).

## Node catalogue

Eight capabilities ship in V0. Three are sources (no inputs); five take exactly
one input. None takes two, and none produces more than one output.

| Capability | Input | Output | Configurable |
|---|---|---|---|
| [`value`](#value) | none (source) | `Bytes`, `Text`, `Bool` or `Int` | required |
| [`json.decode`](#jsondecode) | `Bytes` | `JSONDocument` | rejected |
| [`condition`](#condition) | `JSONDocument` | `Bool` | required |
| [`exec.command`](#execcommand) | none (source) | `Command` | required |
| [`exec`](#exec) | `Command` | `CommandResult` | rejected |
| [`http.build`](#httpbuild) | none (source) | `HTTPRequest` | required |
| [`http.request`](#httprequest) | `HTTPRequest` | `HTTPResponse` | optional |
| [`http.body`](#httpbody) | `HTTPResponse` | `Bytes` | rejected |

### `value`

A typed constant declared entirely in its `with` block. Mainly for tests and
examples.

- **Kind:** source, no inputs.
- **Output:** port `out`, of the type named in `with.type`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `type` | string | yes | One of `Bytes`, `Text`, `Bool`, `Int`. |
| `value` | scalar | yes | The literal, which must fit the declared type. |

`Bytes` is written as a string and converted to bytes.

```yaml
payload:
  uses: value
  with:
    type: Bytes
    value: '{"user": {"name": "ada"}}'
```

The configured type name decides the node's output type, so this step's
descriptor depends on its configuration. That is legal because construction
happens at compile time, before the graph is type checked.

**Compile errors** (all `invalid_input`): `missing_type`, `unknown_type`,
`missing_value`, `bad_literal`. **Run-time errors:** none.

### `json.decode`

Decodes JSON into a document.

- **Input:** port `in`, `Bytes`.
- **Output:** port `out`, `JSONDocument`.
- **Configuration:** none. A `with` block is rejected.

`Root` holds whatever the document was: an object, an array, or a scalar.

```yaml
document:
  uses: json.decode
  needs: [payload]
```

**Run-time errors:** `malformed_json` (`invalid_input`, not retryable). The bytes
are the problem, and the same bytes fail the same way, so retrying is pointless.

This is the only place JSON appears in a running pipeline. JSON is what this node
does, never what the engine does.

### `condition`

Tests a path in a decoded document and reports a verdict.

- **Input:** port `in`, `JSONDocument`.
- **Output:** port `out`, `Bool`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `path` | string | yes | Dot-separated, for example `user.name`. No empty segments. |
| `equals` | scalar | exactly one of | Compares the value at `path`. |
| `exists` | bool | `equals`/`exists` | Tests presence. `exists: false` is a meaningful test. |

```yaml
is_ada:
  uses: condition
  needs: [document]
  with:
    path: user.name
    equals: ada

missing_field_is_false:
  uses: condition
  needs: [document]
  with:
    path: user.nickname
    exists: false
```

Semantics worth knowing:

- **A path that does not exist is not an error.** It is `false` for `exists:
  true`, `true` for `exists: false`, and `false` for `equals`. Asking whether
  something is there is the point of the node.
- **Lookup walks object keys only.** A segment applied to a non-object means the
  path does not exist.
- **Numbers are normalised, types are not coerced.** JSON decodes every number as
  a float, so the YAML integer `3` matches the JSON number `3`. The string `"3"`
  does not match the number `3`.
- **`equals` must be a scalar.** Comparing two maps with `==` would panic at
  execution, so restricting it to scalars keeps the comparison total.
- **It does not branch.** A verdict is data, like any value on an edge. V0 has no
  branching semantics, and putting control flow inside a node instead of in the
  graph would be the wrong place for it.

**Compile errors** (all `invalid_input`): `missing_path`, `bad_path`,
`missing_test`, `ambiguous_test`, `unsupported_equals`, `bad_equals`.
**Run-time errors:** none. Once configured, this node always produces a verdict.

### `exec.command`

Describes a local process to run.

- **Kind:** source, no inputs.
- **Output:** port `out`, `Command`.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `name` | string | yes | | The program. Not resolved through a shell. |
| `args` | list of string | no | empty | Passed verbatim, with no word splitting. |
| `dir` | string | no | engine's own | Working directory. |
| `timeout` | duration string | no | none | Bounds the run. Must be positive. |

```yaml
greeting:
  uses: exec.command
  with:
    name: /bin/echo
    args: [hello, from, p6e]
    timeout: 5s
```

There is no shell. To use shell syntax, run a shell explicitly:
`name: /bin/sh`, `args: ["-c", "echo $HOME"]`.

This node is separate from `exec` because the engine performs no implicit
conversion: `exec` consumes a `Command`, so something must produce one. A future
node that builds a command from data plugs in at exactly the same place.

**Compile errors** (all `invalid_input`): `bad_config`, `missing_name`,
`bad_timeout`. **Run-time errors:** none.

### `exec`

Runs the process.

- **Input:** port `in`, `Command`.
- **Output:** port `out`, `CommandResult`.
- **Configuration:** none. A `with` block is rejected.

```yaml
run:
  uses: exec
  needs: [greeting]
  retry:
    max_attempts: 2
    backoff: 100ms
```

**A non-zero exit code is not a failure.** It arrives as
`CommandResult.ExitCode` on a successful edge, together with `Stdout` and
`Stderr`. Only the workflow knows whether a command that exits 1 is a problem. A
node error means the process never ran, or was killed before it could finish.

| Code | Kind | Retryable | When |
|---|---|---|---|
| `timeout` | `transient` | yes | The command outran its own `timeout`. |
| `not_found` | `permanent` | no | The program does not exist. |
| `start_failed` | `permanent` | no | The process could not be started. |
| `no_command` | `invalid_input` | no | The `Command` has an empty name. |
| `cancelled` | `cancelled` | no | The execution was cancelled or its deadline expired. |

The distinction between the execution being cancelled and the command outrunning
its own budget is deliberate: the first is not worth retrying, the second may be.

After a kill, the engine waits a further 500ms grace. Without it, a backgrounded
grandchild still holding the output pipe keeps the wait blocked, and a step's
timeout would bound nothing.

### `http.build`

Describes an HTTP call.

- **Kind:** source, no inputs.
- **Output:** port `out`, `HTTPRequest`.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | string | yes | | Absolute, `http` or `https`, with a host. |
| `method` | string | no | `GET` | Upper-cased. |
| `headers` | map of string to string | no | none | Sent as given, with no implicit additions. |
| `body` | string | no | empty | The request body. |

```yaml
request:
  uses: http.build
  with:
    method: GET
    url: https://api.github.com/repos/golang/go
    headers:
      Accept: application/vnd.github+json
```

The URL is validated at compile time, so a typo fails `p6e check` rather than a
production run.

**Compile errors** (all `invalid_input`): `bad_config`, `missing_url`, `bad_url`
(unparseable, wrong scheme, or no host). **Run-time errors:** none.

### `http.request`

Makes the call.

- **Input:** port `in`, `HTTPRequest`.
- **Output:** port `out`, `HTTPResponse`.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `timeout` | duration string | no | `30s` | Bounds one call end to end, connection through body read. Must be positive. |
| `max_body_bytes` | int | no | `10485760` (10 MiB) | Caps the body a step will hold. Must be positive. |

```yaml
fetch:
  uses: http.request
  needs: [request]
  with:
    timeout: 10s
    max_body_bytes: 1048576
  retry:
    max_attempts: 3
    backoff: 200ms
```

**A non-2xx status is not a failure.** It arrives as `Response.Status` on a
successful edge. Whether a 404 means the workflow failed is a decision only the
workflow can make, typically with a `condition` step reading the field. A node
error means no response was obtained at all.

| Code | Kind | Retryable | When |
|---|---|---|---|
| `timeout` | `transient` | yes | The call exceeded `timeout`. |
| `transport` | `transient` | yes | Connection refused, DNS failure, TLS failure. |
| `body_read` | `transient` | yes | The response body could not be read to the end. |
| `body_too_large` | `permanent` | no | The body exceeded `max_body_bytes`. |
| `invalid_request` | `permanent` | no | The method or URL cannot form a request. |
| `unsupported_scheme` | `permanent` | no | The scheme is not `http` or `https`. |
| `cancelled` | `cancelled` | no | The execution was cancelled or its deadline expired. |

An oversized body is detected rather than truncated: the reader is given the
limit plus one byte, so a body exactly at the limit still reads and one byte over
is an error instead of silent data loss.

All steps share one HTTP transport, so connection pooling, keep-alive and TLS
session reuse work across steps. `MaxIdleConnsPerHost` is raised to 32 from the
standard library's 2, because a pipeline typically fans out against one host and
two idle connections would force a reconnect on nearly every step. The client is
built at compile time and shared by every execution of the step, which is what
makes that reuse possible.

**Compile errors:** `bad_config` (timeout not a duration or not positive,
`max_body_bytes` negative).

### `http.body`

Extracts the body from a response.

- **Input:** port `in`, `HTTPResponse`.
- **Output:** port `out`, `Bytes`.
- **Configuration:** none. A `with` block is rejected.

```yaml
body:
  uses: http.body
  needs: [fetch]
```

A one-line extractor that exists because the engine performs no implicit
conversion between types. A pipeline feeding an HTTP response into
`json.decode`, which takes `Bytes`, must say so with a step, so the graph
describes the whole computation instead of hiding part of it in the engine.

The body is shared, not copied: the `Bytes` it produces points at the same
backing array as the response, which is safe because values are immutable.

**Run-time errors:** none.

## Errors

Every node failure is a `NodeError` with a fixed vocabulary, never a panic used
as control flow. A panic at the node boundary is recovered and normalised to
`internal`.

| Kind | Meaning | Retryable by default |
|---|---|---|
| `transient` | The same call might succeed if repeated: a timeout, a 503, lock contention. | yes |
| `permanent` | Repeating the call fails the same way: a 404, a missing binary, a rejected credential. | no |
| `invalid_input` | The input the node received is not usable. A bug in the pipeline, not a condition in the world. | no |
| `cancelled` | The execution was cancelled or its deadline expired. | no |
| `internal` | The node or the engine broke. Recovered panics land here. | no |

A `NodeError` also carries a `Code` (a node-specific identifier, stable enough to
match on, listed per node above) and a human-facing `Message`.

An unknown Go error that a node forwards without classifying is treated as
`permanent`, because guessing that an unknown failure is retryable turns one
failure into several.

## Execution semantics

**Concurrency.** Every step whose dependencies have all succeeded is ready, and
ready steps run concurrently, one goroutine each. Concurrency is capped at 256
by default. `run --inline` runs a solitary ready step on the coordinator
goroutine instead, which is much faster on sequential pipelines (ADR 0008).

**Fan-out shares, never copies.** Several steps consuming one step's output all
receive the same pointer.

**First failure stops the run.** When a step fails, the execution stops
scheduling new work.

**Skipped steps.** Anything that never started because a dependency failed, or
because the execution stopped first, ends as `skipped`. It is not an error in
itself.

**Cancellation and abandonment.** The executor honours its context. A step still
running 5 seconds after the execution stopped is abandoned and marked
`cancelled`, so one node ignoring cancellation cannot wedge the process (ADR
0004).

**Step states:** `succeeded`, `failed`, `cancelled`, `skipped`.

**Plans are reusable.** A compiled plan is immutable and safe to run many times
concurrently; all per-execution state lives in the executor. Nodes are stateless
with respect to workflows: one implementation serves many concurrent executions.

## Compile error reference

Everything below is reported by `p6e check`, before anything runs. Compilation
reports as many problems as it can in one pass, because fixing one error at a
time and recompiling between each is a miserable way to write a pipeline.

| Problem | Example message |
|---|---|
| Unsupported version | `unsupported version 2 (this build understands 1)` |
| No steps | `pipeline has no steps` |
| Missing `uses` | `step "fetch": missing uses` |
| Unknown capability | `step "fetch": unknown node "http.reqest" (known: [condition exec exec.command http.body http.build http.request json.decode value])` |
| Invalid configuration | `step "payload": invalid configuration for "value": unknown value type "Byte" (accepted: Bytes, Text, Bool, Int) [invalid_input/unknown_type]` |
| Unknown `with` field | `step "a": invalid configuration for "json.decode": yaml: unmarshal errors:` followed by `line 1: field foo not found in type struct {}` on the next line |
| Self-reference | `step "a" needs itself` |
| Missing dependency | `step "b": needs "a", which is not a step in this pipeline` |
| Dependency cycle | `dependency cycle: "a" needs "b" needs "c" needs "a"` |
| Wrong input count | `step "j": node "join" expects 2 input(s) (Alpha, Beta) but needs lists 1` |
| Type mismatch | `step "document": input "in" expects Bytes but step "payload" produces Text` |
| Unbound input | `step "j": input "in1" of node "join" is not bound by needs (inputs: "in0", "in1")` |
| Unknown port | `step "j": needs binds "in2", but node "join" has no such input (inputs: "in0", "in1")` |
| Ambiguous positional binding | `step "p": node "pair" has inputs of identical type Alpha ("in0", "in1"), so a positional swap would type check: bind needs by name instead` |
| Retry out of range | `step "f": retry.max_attempts must be at most 100, got 500` |

Two things about that table are worth knowing before you try to reproduce a row.

**The four rows about input binding use nodes that are not in the catalogue.**
`join` is `(Alpha, Beta) -> Beta` and `pair` is `(Alpha, Alpha) -> Beta`, both
test fixtures. No built-in node takes more than one input, so those four errors
cannot be produced with the bundled nodes at all. They are documented because
they apply the moment you add a node that takes two.

**A cycle is usually reported alongside other problems**, because every compile
phase runs and each skips only the steps it genuinely cannot judge. A cycle of
steps whose types do not line up reports both, which is more useful than hiding
one behind the other but longer than the single line above suggests:

```text
cycle.yaml: 4 problems:
  dependency cycle: "a" needs "c" needs "b" needs "a"
  step "a": input "in" expects Bytes but step "c" produces JSONDocument
  step "b": input "in" expects Bytes but step "a" produces JSONDocument
  step "c": input "in" expects Bytes but step "b" produces JSONDocument
```

## CLI

```
p6e check <pipeline.yaml>   compile and validate without running
p6e run   <pipeline.yaml>   compile, then execute
p6e nodes                   list the available node capabilities
```

Options for `run`:

| Option | Effect |
|---|---|
| `--detect-mutation` | Report nodes that mutate a value they do not own. Expensive: for debugging, not production. |
| `--inline` | Run a solitary ready step on the main goroutine. Much faster on sequential pipelines, but a node that ignores cancellation will wedge the run instead of being abandoned. |

Exit codes: `0` success, `1` a broken pipeline or a failed run, `2` a broken
invocation.

## A complete example

`examples/http.yaml`, which is four steps for what other engines do in one,
on purpose: the graph describes the whole computation, so nothing converts a
response into bytes behind your back.

```yaml
version: 1

steps:
  request:
    uses: http.build
    with:
      method: GET
      url: https://api.github.com/repos/golang/go
      headers:
        Accept: application/vnd.github+json

  fetch:
    uses: http.request
    needs: [request]
    with:
      timeout: 10s
      max_body_bytes: 1048576
    retry:
      max_attempts: 3
      backoff: 200ms

  body:
    uses: http.body
    needs: [fetch]

  document:
    uses: json.decode
    needs: [body]

  is_public:
    uses: condition
    needs: [document]
    with:
      path: private
      equals: false
```

Typed end to end: `() -> HTTPRequest -> HTTPResponse -> Bytes -> JSONDocument ->
Bool`. Change `equals: false` to a path that does not exist and it still
compiles, because a missing path is a verdict rather than an error. Change
`needs: [fetch]` on `document` to skip `http.body` and it does not, because
`json.decode` takes `Bytes` and `fetch` produces `HTTPResponse`.

The other bundled examples are `examples/json.yaml` (fan-out to three
conditions sharing one decoded document), `examples/exec.yaml` (running a local
process), and `examples/broken.yaml` (a deliberate one-character type error, to
show what rejection looks like).

## See also

- `docs/adr/0001-type-bridge.md`: how typed Go values cross an edge.
- `docs/adr/0002-input-binding.md`: why `needs` binds positionally.
- `docs/adr/0005-named-input-binding.md`: the mapping form.
- `docs/adr/0009-mandatory-named-binding.md`: when the mapping form is required.
- `docs/adr/0004-bounding-the-executor.md`: concurrency caps and abandonment.
- `docs/adr/0006-detecting-mutation.md`: `--detect-mutation`.
- `docs/adr/0008-inline-solo-steps.md`: `--inline`.
- `README.md`: the engine's design, and how to write a node.

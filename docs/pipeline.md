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
- [Inputs](#inputs)
- [Trigger](#trigger)
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

inputs:                          # optional
  <name>: <type>

trigger:                         # optional
  uses: <trigger capability>
  with: <trigger configuration>
  timeout: <duration>
  respond_with: <step-id>
  on_overlap: allow | drop

steps:
  <step-id>:
    uses: <capability>
    needs: <list or mapping>     # optional
    with: <node configuration>   # optional
    retry:                       # optional
      max_attempts: <int>
      backoff: <duration>
```

| Key | Required | Value |
|---|---|---|
| `version` | yes | Must be `1`. It is the only schema version this build understands. |
| `inputs` | no | A mapping of input name to type name. See [Inputs](#inputs). |
| `trigger` | no | What starts a run when the pipeline is served. See [Trigger](#trigger). |
| `steps` | yes | A mapping of step ID to step. Must contain at least one entry. |

Two document-level rules:

- **Decoding is strict.** An unknown field anywhere is an error, not something
  ignored. A typo that is silently dropped produces a pipeline that checks clean
  and then does something other than what it says.
- **A document is capped at 1 MiB** (`MaxPipelineBytes`). Input over the limit
  is an error rather than a truncation, because a silently truncated pipeline
  would compile into a different, smaller graph.

## Inputs

Optional. Declares values the run supplies, so one pipeline serves many runs
instead of being a constant.

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

**An input is a graph node like any other.** A step binds it with `needs` exactly
as it binds a step, and the compiler type checks that edge the same way. The
only difference is where the value comes from.

Because `needs` cannot say which it meant, **inputs and steps share one
namespace**: a name used by both is a parse error.

### What is checked, and when

| Checked | When |
|---|---|
| The declared type exists | compile time |
| Every step consuming an input expects the declared type | compile time |
| An input's name does not collide with a step | parse time |
| A supplied value carries the declared type | run time |
| Every declared input was supplied | run time |

The last two are the only checks in the engine that cannot happen before a run,
and they are unavoidable: the compiler cannot know a value it was never given.
Both are bounded, because they happen before any node executes.

**`p6e check` needs no values.** That is the point: a pipeline whose inputs are
secrets still validates on a machine that does not hold them.

### Supplying values

`--input NAME=VALUE`, repeated once per input. `--input NAME=@FILE` reads the
value from a file. The declared type decides how the text is read:

| Declared type | Read as |
|---|---|
| `Text` | the text itself |
| `Bytes` | its bytes |
| `Int` | a whole number |
| `Bool` | `true` or `false` |
| `Time` | an RFC 3339 instant, such as `2026-08-13T09:00:00Z` |

`Time` is there so a scheduled pipeline can be run by hand: the daemon supplies
the instant its schedule fired, and testing that pipeline offline means being
able to name one.

Those are what the command line can build. The engine accepts an input of
any registered type, which an embedded caller can supply directly through
`Options.Inputs`. A pipeline wanting a document from the command line declares
`Bytes` and adds a `json.decode` step, which is the same explicit conversion
every other edge makes.

An assignment naming an input the pipeline did not declare is an error, not
something ignored: a misspelled name would otherwise look like it worked while
the real input went unsupplied.

### Inputs and `env.get`

Both bring a value in from outside, and they differ in who supplies it. An input
is for what varies **per run**, and the caller provides it. [`env.get`](#envget)
is for what varies **per environment**, and the machine provides it.

### Limits

Every declared input is required; there is no default. All of them are supplied
per run, so a plan cannot carry one from a previous execution.

## Trigger

A trigger is what starts a run when the pipeline is served by `p6e serve`. A
pipeline without one is perfectly valid: it is simply run by hand, and `serve`
skips it.

**A trigger is not a step.** It does not appear in the graph and nothing
`needs` it. What it does is supply the values declared under
[`inputs`](#inputs), and the compiler proves it supplies every one of them at
the declared type. Nothing about an event's shape is left to be discovered on
the first request.

```yaml
inputs:
  body: Bytes                    # trigger.webhook supplies this as Bytes

trigger:
  uses: trigger.webhook
  with:
    path: /hooks/deploy
    method: POST
  timeout: 30s
  respond_with: reply
  on_overlap: allow
```

| Key | Required | Value |
|---|---|---|
| `uses` | yes | A trigger capability. `p6e triggers` lists them. |
| `with` | no | The trigger's configuration, decoded strictly by the trigger itself. |
| `timeout` | for webhooks | Bounds one run. Required for a trigger that answers a caller, because an unbounded run holds that caller's connection open indefinitely. |
| `respond_with` | no | The step whose output becomes the reply. Only meaningful for a trigger that has somebody to answer. |
| `on_overlap` | no | `allow` or `drop`. Defaults per kind of trigger, see below. |

A pipeline may declare **at most one trigger**. Two triggers feeding one graph
would mean a step accepting either payload type, and the type system is nominal
with no union type and no `Any`. Two triggers means two files.

### Testing a triggered pipeline

Because a trigger only supplies inputs, a triggered pipeline runs by hand with
no daemon and no traffic:

```bash
p6e run --input body=@event.json pipeline.yaml
```

This is the intended way to exercise one in CI. There is no separate facility
for firing a trigger synthetically, because supplying inputs already is one.

### Overlap

What happens when a trigger fires while a run of the same pipeline is still
going.

| Policy | Effect |
|---|---|
| `allow` | Start the new run alongside the one in flight. |
| `drop` | Refuse the event and let the run in flight finish. |

The default depends on the kind of trigger. One that answers a caller defaults
to `allow`, because that caller is already waiting on its own event and refusing
it for an unrelated one would be surprising. Everything else defaults to `drop`,
which is the cron convention and the only default that cannot pile runs up
faster than they finish.

There is no queue. A queue turns a fast rejection into a slow timeout, and an
unbounded one is how a daemon dies.

### Responding

`respond_with` names the step whose output is written back. That step must
produce `Bytes` or `Text`: turning a structure into bytes is a step's job, so a
pipeline answering JSON ends in `json.encode`. Naming a step that produces
anything else is a compile error, as is naming an input, a step that does not
exist, or using `respond_with` at all on a trigger with nobody to answer.

Replies are synchronous. There is no "202 plus an identifier" mode, because
collecting a result later means storing executions and the daemon keeps nothing
after a run ends. That is deferred rather than refused: see ADR 0012 for what it
would take and what is already shaped to allow it.

### `trigger.webhook`

Runs the pipeline once per matching HTTP request.

- **Kind:** answers a caller, so `timeout` is required and `respond_with` is
  available.
- **Claim:** the method and path, for example `POST /hooks/deploy`. Two served
  pipelines cannot claim the same one.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `path` | string | yes | | Must start with `/`. |
| `method` | string | no | `POST` | One of GET, POST, PUT, PATCH, DELETE. Case insensitive. |
| `max_body` | int | no | 1 MiB | Bytes. A larger body is refused before any step runs. |
| `auth` | block | no | none | Verifies every event's signature. Absent means the route is open. |

**A webhook with no `auth` block authenticates nothing.** Anyone who can reach
the listener can start the run, so the daemon logs a warning at startup naming
every open route. Either configure `auth` here or front the listener with a
proxy that authenticates.

`auth` fields:

| Field | Type | Required | Notes |
|---|---|---|---|
| `scheme` | string | yes | Only `hmac-sha256` exists: HMAC-SHA256 over the raw body, hex encoded. |
| `header` | string | yes | Where the signature arrives, for example `X-Hub-Signature-256`. |
| `prefix` | string | no | What the sender puts before the digest, for example `sha256=`. Empty means the header holds the digest alone. |
| `secret_env` | string | yes | Names the environment variable holding the shared secret. |

```yaml
trigger:
  uses: trigger.webhook
  with:
    path: /hooks/deploy
    auth:
      scheme: hmac-sha256
      header: X-Hub-Signature-256
      prefix: "sha256="
      secret_env: DEPLOY_WEBHOOK_SECRET
  timeout: 30s
```

The secret is **named, not inlined**. A pipeline directory is deployed and read
like a crontab, which is the wrong place for a credential, and naming a variable
is also what keeps `p6e check` free of secrets: the block is validated at
compile time and the variable is read per request, so a pipeline whose secret
exists only in production still validates anywhere.

A rejected event answers `401` with the body `unauthorized`, and nothing else.
Which half of the signature was wrong distinguishes a missing header from a bad
digest, and telling the sender that tells an attacker which half to work on, so
it goes to the daemon's log instead. An unset `secret_env` is a broken daemon
rather than an unauthorized caller: it answers `500` and names the variable.

| Code | Status | When |
|---|---|---|
| `unauthorized` | 401 | No signature, a malformed one, or one that does not match the body. |
| `secret_unset` | 500 | `secret_env` is unset or empty in the daemon's environment. |
| `body_too_large` | 400 | The body exceeded `max_body`. Checked before the signature, since the signature is computed over the body. |

Supplies:

| Name | Type | Value |
|---|---|---|
| `body` | `Bytes` | The request body. |
| `method` | `Text` | The request method. |
| `path` | `Text` | The request path. |
| `query` | `Text` | The raw query string. |

Declare only what the pipeline consumes; the rest are unused.

The inbound request is deliberately **not** supplied as an `HTTPRequest`. That
type describes a call to make, and an inbound request is not one; sharing it
would let a pipeline forward whatever arrived straight back out.

### `trigger.schedule`

Runs the pipeline once per interval.

- **Kind:** answers nobody, so `timeout` is optional and `respond_with` is
  rejected.
- **Claim:** none. Any number of schedules coexist.

| Field | Type | Required | Notes |
|---|---|---|---|
| `every` | duration string | yes | At least 1ms. |

Supplies:

| Name | Type | Value |
|---|---|---|
| `fired_at` | `Time` | The instant the tick happened. |

There is no cron syntax. Cron means a parser and a timezone database, which is a
dependency and a decision of its own.

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

Optional. Declares which steps, or [inputs](#inputs), feed this step's input
ports. A step with no `needs` is a root and starts immediately.

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

The rule fires most often on [`text.format`](#textformat), whose ports are all
`Text`, so any template with two or more placeholders must be bound by name. The
other multi-input built-ins, `http.with_header` and `http.with_body`, take ports
of distinct types and stay bindable either way. See ADR 0002, ADR 0005 and
ADR 0009.

### Port names

Port names come from the node's descriptor, not from the pipeline. The adapters
in `internal/node` generate them:

| Adapter | Input ports | Output port |
|---|---|---|
| `NewSource` | none | `out` |
| `NewTypedNode` | `in` | `out` |
| `NewTypedNode2` | `in0`, `in1` | `out` |
| `NewTypedNodeN` | named by the node | `out` |

So the named form of a single-input built-in is `needs: {in: fetch}`. It is
legal and equivalent to `needs: [fetch]`, and it is not worth writing: a single
port cannot be bound to the wrong thing.

`NewTypedNodeN` is how a node's arity comes from its configuration, which is
what lets `text.format` name a port after each placeholder in its template. Its
ports all carry one type.

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

Where a value has to be built from data, a node does it and the graph shows it:
[`text.format`](#textformat) composes a string from typed input ports, and
[`http.from_url`](#httpfrom_url), [`http.with_header`](#httpwith_header) and
[`http.with_body`](#httpwith_body) assemble a request from edges. Those keep the
convenience of interpolation while the compiler still checks every value's type
and presence.

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

Twenty-two capabilities ship in V0. None produces more than one output.

| Capability | Input | Output | Configurable |
|---|---|---|---|
| [`value`](#value) | none (source) | `Bytes`, `Text`, `Bool` or `Int` | required |
| [`env.get`](#envget) | none (source) | `Text`, `Bytes`, `Bool` or `Int` | required |
| [`text.format`](#textformat) | one `Text` per placeholder | `Text` | required |
| [`json.decode`](#jsondecode) | `Bytes` | `JSONDocument` | rejected |
| [`json.encode`](#jsonencode) | `JSONDocument` | `Bytes` | rejected |
| [`json.get`](#jsonget) | `JSONDocument` | `Text`, `Bytes`, `Bool` or `Int` | required |
| [`condition`](#condition) | `JSONDocument` | `Bool` | required |
| [`assert.true`](#asserttrue) | `Bool` | `Bool` | optional |
| [`exec.command`](#execcommand) | none (source) | `Command` | required |
| [`exec`](#exec) | `Command` | `CommandResult` | rejected |
| [`exec.stdout`](#execstdout-execstderr-execexit_code) | `CommandResult` | `Bytes` | rejected |
| [`exec.stderr`](#execstdout-execstderr-execexit_code) | `CommandResult` | `Bytes` | rejected |
| [`exec.exit_code`](#execstdout-execstderr-execexit_code) | `CommandResult` | `Int` | rejected |
| [`http.build`](#httpbuild) | none (source) | `HTTPRequest` | required |
| [`http.request`](#httprequest) | `HTTPRequest` | `HTTPResponse` | optional |
| [`http.body`](#httpbody) | `HTTPResponse` | `Bytes` | rejected |
| [`http.status`](#httpstatus) | `HTTPResponse` | `Int` | rejected |
| [`http.header`](#httpheader) | `HTTPResponse` | `Text` | required |
| [`http.from_url`](#httpfrom_url) | `Text` | `HTTPRequest` | optional |
| [`http.with_header`](#httpwith_header) | `HTTPRequest`, `Text` | `HTTPRequest` | required |
| [`http.with_body`](#httpwith_body) | `HTTPRequest`, `Bytes` | `HTTPRequest` | rejected |
| [`http.assert_status`](#httpassert_status) | `HTTPResponse` | `HTTPResponse` | required |

**Extractors are load-bearing.** A node has exactly one output, so a node
producing several values bundles them into one type: `CommandResult` carries
three, `HTTPResponse` carries three. `exec.stdout`, `exec.exit_code`,
`http.status`, `http.header` and `http.body` are what put an individual field on
an edge. Without them those bundled values are unreachable and the nodes that
produce them are dead ends, so these are not conveniences: they are how the
catalogue connects to itself.

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

### `env.get`

Reads an environment variable, so a pipeline can reach a token or an endpoint
without hardcoding it.

- **Kind:** source, no inputs.
- **Output:** port `out`, of the type named in `with.as`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | The variable to read. |
| `as` | string | yes | One of `Text`, `Bytes`, `Bool`, `Int`. Becomes the output type. |
| `default` | string | no | Produced when the variable is unset. Declaring none makes an unset variable an error. |

```yaml
token:
  uses: env.get
  with:
    name: GITHUB_TOKEN
    as: Text
```

**The variable is read at execution, not at compile time.** That matters twice:
`p6e check` stays runnable on a machine without the secrets present, and one
compiled plan run in two environments sees each one's values rather than
whichever was in scope when it compiled. It also keeps secrets out of the plan.

Configuration is still validated at compile time, including whether a declared
default parses as the declared type.

**Compile errors** (all `invalid_input`): `missing_name`, `missing_type`,
`unknown_type`, `bad_default`. **Run-time errors** (all `permanent`, not
retryable): `env_absent` when unset with no default, `bad_value` when the value
does not parse as the declared type. A set-but-unparseable value fails rather
than falling back to the default: the default covers absence, not corruption.

### `text.format`

Builds a string from pipeline data. This is what a pipeline uses instead of
interpolation in a `with` block.

- **Input:** one `Text` port per placeholder, named after it.
- **Output:** port `out`, `Text`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `template` | string | yes | Literal text with `{{name}}` placeholders. |

```yaml
url:
  uses: text.format
  with:
    template: "https://api.github.com/repos/{{owner}}/{{name}}/releases"
  needs:
    owner: repo_owner
    name: repo_name
```

The template is parsed at compile time and **its placeholders become the node's
input ports**, in order of first appearance. That is what makes this
interpolation the compiler checks: a placeholder with nothing bound to it is a
compile error, and a `needs` entry naming a placeholder the template does not
contain is too. Both are the ordinary named-binding checks, applied to ports
that happen to have come from a string.

Every port is a `Text`, so **a template with more than one placeholder cannot be
bound positionally**: the rule above requires the mapping form exactly when
ports share a type, and here they always do.

Other semantics:

- A name repeated in the template is **one port**, used at each occurrence.
- Whitespace inside a placeholder is trimmed, so `{{ name }}` binds `name`.
- A template with no placeholder is a constant, and legal.

**Compile errors** (all `invalid_input`): `missing_template`, and `bad_template`
for an unclosed `{{`, an empty placeholder, or whitespace inside one.
**Run-time errors:** none.

### `json.get`

Reads a value out of a document, so it can be used rather than only tested.

- **Input:** port `in`, `JSONDocument`.
- **Output:** port `out`, of the type named in `with.as`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `path` | string | yes | Dot-separated, as `condition`'s is. |
| `as` | string | yes | One of `Text`, `Bytes`, `Bool`, `Int`. Becomes the output type. |
| `default` | scalar | no | Produced when the path is absent. Declaring none makes an absent path an error. |

```yaml
owner:
  uses: json.get
  needs: [document]
  with:
    path: repo.owner
    as: Text
```

`condition` answers a question about a document; this reads a value out of one.
The declared type becomes the step's output type, which is what keeps extraction
statically typed without a structural type system: the pipeline states the type
it expects, and the compiler checks every use of it.

**Conversion is explicit and never coerces.** A JSON string does not satisfy
`as: Int`, and a JSON number does not satisfy `as: Text`. A JSON number does
satisfy `as: Int` when it has no fractional part, because JSON has no integer
type of its own.

A declared default is converted at compile time by the same rules, so a default
that does not fit `as` fails `p6e check`. It covers an **absent path only**: a
path that exists but holds the wrong type is a mismatch, not an absence, and
fails rather than falling back.

**Compile errors** (all `invalid_input`): `missing_path`, `bad_path`,
`missing_type`, `unknown_type`, `bad_default`. **Run-time errors** (all
`invalid_input`, not retryable): `path_absent`, `type_mismatch`.

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

### `json.encode`

Encodes a document back to bytes.

- **Input:** port `in`, `JSONDocument`.
- **Output:** port `out`, `Bytes`.
- **Configuration:** none. A `with` block is rejected.

`json.decode`'s inverse, and the only way a pipeline can produce a JSON payload
rather than only consume one.

```yaml
payload:
  uses: json.encode
  needs: [document]
```

**Run-time errors:** `unencodable` (`invalid_input`, not retryable). A document
that came from `json.decode` always encodes; one assembled by another node need
not, so the failure is reported rather than assumed away.

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
  graph would be the wrong place for it. To act on a verdict, feed it to
  [`assert.true`](#asserttrue), which turns a false one into a failed run.

**Compile errors** (all `invalid_input`): `missing_path`, `bad_path`,
`missing_test`, `ambiguous_test`, `unsupported_equals`, `bad_equals`.
**Run-time errors:** none. Once configured, this node always produces a verdict.

### `assert.true`

Turns a verdict into the run's outcome.

- **Input:** port `in`, `Bool`.
- **Output:** port `out`, the same `Bool`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `message` | string | no | Replaces the default failure text. It is what a person reads when the run fails, so it should say what was expected. |

```yaml
require_healthy:
  uses: assert.true
  needs: [is_healthy]
  with:
    message: service is not reporting status ok
```

A false verdict fails the step, which stops the run and gives the process a
non-zero exit code. That is what makes a pipeline usable from cron or CI, where
the exit code is the whole interface.

**This is the engine's only conditional execution.** There is no branching: a
`Bool` is data, and nothing consumes it as control flow. What exists instead is
that a failed step stops the run, and this node is the bridge to it. The verdict
passes through rather than being consumed, so a step that must run only after an
assertion holds takes that `Bool` as its input:

```yaml
notify:
  uses: exec
  needs: [require_healthy, command]   # never runs if the assertion failed
```

What this deliberately does not cover is "carry on quietly if the verdict is
false". Suppressing part of a graph without failing needs a skipped terminal
state the scheduler honours, which is engine work and is not built.

**There is no `assert.false`.** A negative test belongs in the node that
produced the verdict, where `condition` already expresses it with `equals` or
with `exists: false`.

**Run-time errors:** `assertion_failed` (`permanent`, not retryable). Re-testing
the same verdict reaches the same answer; retrying the work that produced it is
that step's own `retry` policy.

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

A node has one output, so all three fields arrive bundled in one
`CommandResult`. `exec.stdout`, `exec.stderr` and `exec.exit_code` are what put
each of them on an edge of its own.

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

### `exec.stdout`, `exec.stderr`, `exec.exit_code`

Read one field of a `CommandResult` onto an edge.

| Capability | Input | Output |
|---|---|---|
| `exec.stdout` | `CommandResult` | `Bytes` |
| `exec.stderr` | `CommandResult` | `Bytes` |
| `exec.exit_code` | `CommandResult` | `Int` |

All three take no configuration; a `with` block is rejected. None can fail.

```yaml
run:
  uses: exec
  needs: [cmd]

output:
  uses: exec.stdout
  needs: [run]

code:
  uses: exec.exit_code
  needs: [run]
```

`exec` bundles everything a process did into one value because a node has one
output. These are what unbundle it, and without them nothing downstream can
consume an `exec` step at all.

The bytes are shared, not copied: `exec.stdout` points at the same backing array
as the result, which is safe because values are immutable. Both extractors can
read the same result concurrently, since fan-out shares one reference.

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
| `allow_private` | bool | no | `false` | Permits internal destinations. Off by default: see below. |

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
workflow can make, and `http.status` is what puts the code on an edge so it can
make it. A node error means no response was obtained at all.

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

**Internal destinations are refused by default.** A request can be built from
data: `http.from_url` takes its URL off an edge, and that edge can carry a
webhook body, so an outbound call is reachable by whoever sent the event.
Without a policy the daemon is a confused deputy, sitting inside a network the
caller cannot reach and fetching whatever it is told to. These are refused:

| Range | Why |
|---|---|
| `127.0.0.0/8`, `::1` | Loopback, which includes p6e's own admin listener. |
| `169.254.0.0/16`, `fe80::/10` | Link-local, where cloud metadata (`169.254.169.254`) lives. |
| `10/8`, `172.16/12`, `192.168/16`, `fc00::/7` | Private and unique-local. |
| `0.0.0.0`, `::` | Unspecified. |
| Multicast | Not a call destination. |

Set `allow_private: true` on the step that is meant to reach a service inside
the deployment. It is a decision about one step, not the process.

The check runs **in the dialer**, on the address actually being connected to,
not on the URL string. That is what makes DNS rebinding a non-issue: there is no
window between resolving a hostname and connecting to it in which a second DNS
answer could return a different address. It also covers every redirect hop,
since each hop dials through the same transport. Redirects are additionally
capped at 10 and re-checked for scheme, because otherwise a redirect is a hole
in `http.build`'s compile-time URL check: the configured URL is validated and
the location a server returns is not.

A refused destination surfaces as `transport`, which is retryable. That is
deliberate: the dial genuinely failed, and a pipeline pointed at a name whose
resolution is being fixed should get its retries.

Steps share one HTTP transport **per destination policy**, so connection
pooling, keep-alive and TLS session reuse work across every step that made the
same choice. `MaxIdleConnsPerHost` is raised to 32 from the standard library's
2, because a pipeline typically fans out against one host and two idle
connections would force a reconnect on nearly every step. The client is built at
compile time and shared by every execution of the step, which is what makes that
reuse possible.

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

### `http.status`

Reads the status code onto an edge.

- **Input:** port `in`, `HTTPResponse`.
- **Output:** port `out`, `Int`.
- **Configuration:** none. A `with` block is rejected.

```yaml
status:
  uses: http.status
  needs: [fetch]
```

This is what makes "a non-2xx status is data" usable. A 404 arrives as the
integer 404, and the pipeline decides what that means.

**Run-time errors:** none.

### `http.header`

Reads one response header onto an edge.

- **Input:** port `in`, `HTTPResponse`.
- **Output:** port `out`, `Text`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | Matched case insensitively. |
| `default` | string | no | Produced when the response carries no such header. Declaring none makes an absent header an error. |

```yaml
content_type:
  uses: http.header
  needs: [fetch]
  with: {name: Content-Type}

retry_after:
  uses: http.header
  needs: [fetch]
  with: {name: Retry-After, default: "0"}
```

Semantics worth knowing:

- **A missing header is an error unless a default is declared.** There is no
  optional `Text`, so producing `""` for an absent header would be
  indistinguishable from a header that is genuinely empty, and that value would
  flow on into a URL or a body unnoticed. Declaring the default makes the intent
  explicit.
- **An empty default is a real choice**, distinct from declaring none.
- **A header present but empty is a value, not an absence.** The default does not
  displace it.
- **A header sent more than once yields its first value.** A pipeline that needs
  all of them wants a different node rather than a surprising one.

**Compile errors:** `missing_name` (`invalid_input`), plus strict field
rejection. **Run-time errors:** `header_absent` (`permanent`, not retryable):
the same response will not grow the header on a retry.

### `http.from_url`

Builds a request whose URL comes from an edge rather than a `with` block.

- **Input:** port `in`, `Text`.
- **Output:** port `out`, `HTTPRequest`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `method` | string | no | Defaults to `GET`. Upper-cased. |

```yaml
request:
  uses: http.from_url
  needs: [computed_url]
```

`http.build` fixes its URL when the pipeline is written, which is right when the
URL is known then and wrong the moment it comes from data. A request with a
computed URL starts here, and `http.with_header` and `http.with_body` add to it.

**The trade is explicit.** `http.build` validates its URL at compile time, and a
URL arriving on an edge cannot be validated until it arrives. Static *type*
checking is unaffected; what is given up is static *value* checking of the URL,
and only for steps that opt into a computed one. The check still happens, as an
`invalid_input` failure at the step that produced the bad URL rather than
somewhere downstream.

**Run-time errors:** `missing_url`, `bad_url` (both `invalid_input`, not
retryable).

### `http.with_header`

Sets one header on a request, taking the value from an edge.

- **Inputs:** port `in0`, `HTTPRequest`; port `in1`, `Text`.
- **Output:** port `out`, `HTTPRequest`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | The header to set. The value comes from the second input. |

```yaml
authorized:
  uses: http.with_header
  with: {name: Authorization}
  needs: [request, token]
```

The name is configuration because it is known when the pipeline is written; the
value is an input because it is not. An existing header of the same name is
replaced, and names are canonicalised, so setting `content-type` over a request
carrying `Content-Type` replaces it rather than producing both.

**It produces a new request rather than modifying the one it received**, because
values on edges are immutable. That matters twice here: the request it consumes
may fan out to a sibling step, and a retried attempt receives the same input as
the first.

**Compile errors:** `missing_name` (`invalid_input`). **Run-time errors:** none.

### `http.with_body`

Sets the body of a request from an edge.

- **Inputs:** port `in0`, `HTTPRequest`; port `in1`, `Bytes`.
- **Output:** port `out`, `HTTPRequest`.
- **Configuration:** none. A `with` block is rejected.

```yaml
final:
  uses: http.with_body
  needs: [authorized, payload]
```

The body is not wrapped, encoded, or given a content type: pairing this with
`http.with_header` is how a pipeline says what the bytes are. Like
`http.with_header` it produces a new request rather than modifying its input.

**Run-time errors:** none.

### `http.assert_status`

Fails the run when a response carries an unacceptable status.

- **Input:** port `in`, `HTTPResponse`.
- **Output:** port `out`, the same `HTTPResponse`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `equals` | int | one of | An exact code. |
| `min` / `max` | int | `equals` or a range | A closed interval. Either end may be omitted. |

```yaml
ok:
  uses: http.assert_status
  needs: [fetch]
  with:
    min: 200
    max: 299
```

A non-2xx status is data, and `http.request` is right not to fail on one: only
the workflow knows whether a 404 is a problem. This is how a workflow says that
it is, without giving up that principle anywhere else. The step is opt-in, and a
pipeline that wants to inspect a 404 simply does not add one.

The response passes through, so the steps that read it can depend on this one
and will not run at all when the status is wrong.

A code outside 100 to 599 is rejected at compile time, because `equals: 20` is a
typo rather than a status a response could carry.

**Compile errors** (all `invalid_input`): `missing_test`, `ambiguous_test`,
`bad_range`, `bad_status`. **Run-time errors:** `unexpected_status`
(`permanent`, not retryable). Retrying the call is `http.request`'s policy, not
this step's.

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
| Input with no type | `input "p": missing type` |
| Input name taken by a step | `input "d" collides with the step of the same name` |
| Unregistered input type | `input "p" declares type "Byte", which is not a registered type` |
| Type mismatch from an input | `step "d": input "in" expects Bytes but pipeline input "p" supplies Text` |

Two more are reported by a run rather than by `p6e check`, because they concern
values the compiler never sees. Both happen before any node executes:

| Problem | Example message |
|---|---|
| Input not supplied | `input "payload" was not supplied` |
| Supplied value of the wrong type | `input "seed" is declared Box but the value supplied is Label` |

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
p6e check --dir <dir>       compile a whole directory, and report two pipelines
                            claiming one route
p6e run   <pipeline.yaml>   compile, then execute
p6e serve <dir>             run every pipeline in a directory that declares a
                            trigger, each when its trigger fires
p6e nodes                   list the available node capabilities
p6e triggers                list the available trigger capabilities
```

Options for `run`:

| Option | Effect |
|---|---|
| `--input NAME=VALUE` | Supply a declared [input](#inputs). `NAME=@FILE` reads it from a file. Repeat once per input. |
| `--detect-mutation` | Report nodes that mutate a value they do not own. Expensive: for debugging, not production. |
| `--inline` | Run a solitary ready step on the main goroutine. Much faster on sequential pipelines, but a node that ignores cancellation will wedge the run instead of being abandoned. |

Options for `serve`:

| Option | Effect |
|---|---|
| `--listen ADDR` | Address for webhook triggers. Default `:8080`. |
| `--admin-listen ADDR` | Address for `/healthz`, `/readyz` and `/metrics`. Default `127.0.0.1:8081`; `-` serves none of them. |
| `--max-concurrency N` | Steps in flight across every pipeline at once, not pipelines. Default 256. |
| `--drain DURATION` | How long to wait for runs in progress on shutdown. Default 30s. |

Exit codes: `0` success, `1` a broken pipeline or a failed run, `2` a broken
invocation.

### `check --dir` against `serve`

Both compile a directory and both report the same problems. They differ in what
a problem means:

| | A file that will not compile | Two pipelines claiming one route |
|---|---|---|
| `serve` | Logged and skipped, the rest are served | Both rejected, the rest are served |
| `check --dir` | Fails | Fails |

`serve` keeps going because one typo must not stop every unrelated webhook from
answering. `check --dir` fails because it is the CI gate, and a route collision
is the one problem no single-file check can see: without it, a collision is only
discoverable by deploying and noticing that a webhook stopped firing.

Rejecting *both* claimants rather than picking one is deliberate. Serving
whichever sorted first would mean a pipeline quietly answering requests meant
for its neighbour, and the only symptom would be the neighbour never running.

### Serving

`p6e serve` starts one listener for every webhook pipeline, routing by claim,
and a timer per schedule. On SIGTERM or SIGINT it stops accepting events, lets
the runs already going finish, and gives up after `--drain`.

A pipeline whose runs abandon a step three times in a row is quarantined: it
stops firing and the reason is logged. An abandoned step is one still running
after its run gave up on it, which happens when a node ignores its context; the
goroutine cannot be stopped and, unlike a CLI run, a daemon does not exit
shortly afterwards.

### Admin endpoints

On their own listener, defaulting to `127.0.0.1:8081`.

| Path | Meaning |
|---|---|
| `GET /healthz` | The process is running. Nothing else: a daemon whose pipelines are all quarantined still answers `200`, because restarting it is a person's decision and a failing liveness probe would only make an orchestrator loop. |
| `GET /readyz` | The daemon can still do useful work. `503` while draining, and `503` when every served pipeline is quarantined. |
| `GET /metrics` | Prometheus text format: runs, failures, abandoned runs, in-flight and quarantine state per pipeline, plus the shared step budget. |

They are on a separate listener for two reasons. A pipeline claims a method and
a path, so sharing a mux would let one claim `POST /metrics` and shadow this, or
be shadowed by it, and the loser would simply never fire. And the webhook
listener is the one exposed to whatever sends the events, which is not somewhere
operational detail about every pipeline in the process belongs.

A schedule-only daemon has no webhook listener at all, so this is then the only
way to see whether it is alive.

Quarantine is the metric worth alerting on: `p6e_pipeline_quarantined` going to
`1` means that pipeline will not run again until the daemon restarts.

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

The other bundled examples are `examples/monitor.yaml` (a cron-ready health
check that fails the run when a service is unhealthy),
`examples/parameterized.yaml` (a pipeline that takes arguments),
`examples/chaining.yaml`
(using one call's result to build the next, with a computed URL and a token from
the environment), `examples/json.yaml` (fan-out to three conditions sharing one
decoded document), `examples/exec.yaml` (running a local process), and
`examples/broken.yaml` (a deliberate one-character type error, to show what
rejection looks like).

## See also

- `docs/adr/0001-type-bridge.md`: how typed Go values cross an edge.
- `docs/adr/0002-input-binding.md`: why `needs` binds positionally.
- `docs/adr/0005-named-input-binding.md`: the mapping form.
- `docs/adr/0009-mandatory-named-binding.md`: when the mapping form is required.
- `docs/adr/0004-bounding-the-executor.md`: concurrency caps and abandonment.
- `docs/adr/0006-detecting-mutation.md`: `--detect-mutation`.
- `docs/adr/0008-inline-solo-steps.md`: `--inline`.
- `docs/adr/0012-triggered-pipelines-and-daemon-mode.md`: why a trigger supplies
  inputs rather than being a node, and what `serve` adds.
- `README.md`: the engine's design, and how to write a node.

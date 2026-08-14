# p6e

A typed, low-latency pipeline execution engine in Go.

p6e compiles a YAML pipeline into a statically type-checked DAG, then runs it.
Native Go nodes exchange typed values by reference: no JSON, no IPC, no
reflection on the hot path.

It is a compiler and runtime for pipelines. It is not a workflow automation
product: no visual editor, no SaaS, no library of a hundred integrations. The
claim that distinguishes it from the rest of that category is that an
incompatible edge fails at `p6e check`, before anything runs. Given
`examples/broken.yaml`, a pipeline that hands a `Text` value to a step
expecting `Bytes`:

```
$ p6e check examples/broken.yaml
examples/broken.yaml: step "document": input "in" expects Bytes
  but step "payload" produces Text
```

Nothing executes. In an engine that passes untyped documents between steps,
this mismatch runs, and fails later, somewhere else, with a worse message.

## Quickstart

```bash
$ go build -o bin/p6e ./cmd/p6e
```

`p6e check` compiles a pipeline and reports whether it is valid, without
running it:

```
$ ./bin/p6e check examples/json.yaml
ok: examples/json.yaml compiles (5 steps, 1 starting)
```

`p6e run` compiles, then executes, printing one line per step:

```
$ ./bin/p6e run examples/json.yaml
  succeeded document                 252µs
  succeeded has_three                2µs
  succeeded is_ada                   14µs
  succeeded missing_field_is_false   2µs
  succeeded payload                  16µs

ok: 5 steps
```

`examples/json.yaml` decodes a JSON document and fans it out to three
conditions that run concurrently against the same decoded value; the engine
shares a reference, it never copies.

A pipeline can take arguments, so one compiled plan serves many runs rather than
being a constant. Inputs are declared with a type and checked like any other
edge, and `p6e check` still needs no values, which is what lets a pipeline whose
inputs are secrets validate anywhere:

```
$ ./bin/p6e run examples/parameterized.yaml --input owner=golang --input repo=go
```

`p6e nodes` lists the registered capabilities a pipeline file can reference:

```
$ ./bin/p6e nodes
assert.true
condition
env.get
exec
exec.command
exec.exit_code
exec.stderr
exec.stdout
http.assert_status
http.body
http.build
http.from_url
http.header
http.request
http.status
http.with_body
http.with_header
json.decode
json.encode
json.get
text.format
value
```

## How it works

A pipeline has two phases: compile, then run. Compiling turns a YAML file into
an `ExecutionPlan`: it resolves every `uses` name against the node registry,
checks that dependencies exist, detects cycles, binds each step's `needs` to its
node's declared input ports (a list binds positionally, a mapping binds by port
name, and the mapping is mandatory when two ports share a type: ADR 0002, 0005
and 0009), checks that the
type each edge carries matches what the consuming port expects, and decodes
each step's `with` block into the node's config, rejecting unknown fields.
Anything checkable before execution is checked here; the executor consumes a
fully resolved plan and never resolves a name, decodes a config, or walks the
graph.

```
 YAML file --> parse --> compile ---> ExecutionPlan --> run --> Execution
                          |  resolve nodes                |  scheduler
                          |  check deps, cycles            |  goroutine per
                          |  bind needs to ports            |  ready step
                          |  check edge types              |  applies retry
                          |  decode with blocks             |  policy
```

Running a compiled plan schedules a goroutine per ready step and applies each
step's retry policy (see Errors and retry) to whatever the node reports.

## Writing a node

A node is a typed Go function adapted with `node.NewTypedNode`, registered
under a capability name a pipeline can reference. This one takes a `Text` and
returns it upper-cased:

```go
package shout

import (
	"context"
	"strings"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// Definition is the "shout" capability: Text to Text. It takes no
// configuration.
func Definition() node.Definition {
	n := node.NewTypedNode("shout", run)
	return node.Static("shout", n)
}

func run(_ context.Context, _ *node.ExecutionContext, in *types.Text) node.Result[*types.Text] {
	return node.Ok(&types.Text{Value: strings.ToUpper(in.Value)})
}
```

`node.NewTypedNode[I, O any](name string, fn func(context.Context, *node.ExecutionContext, I) node.Result[O]) node.RuntimeNode`
is the authoring entry point; it is also the only place type erasure happens
(ADR 0001). `node.Result[T]` carries exactly one of `Value` or `Err`: build it
with `node.Ok(v)` or `node.Fail(err)`. A failure is a `*node.Error`, never
a panic; it carries a `Kind` (`transient`, `permanent`, `invalid_input`,
`cancelled`, `internal`) and a `Retryable` flag, built with `node.Errf` or
`node.Wrap`.

`node.Definition{Name, New}` is what gets registered: `New(cfg node.Config)
(node.RuntimeNode, error)` runs once per step, at compile time, never during
execution, which is what lets configuration decoding and validation happen
ahead of the hot path. `node.Static(name, n)` is the shortcut for a node that
takes no configuration, and it rejects a stray `with` block rather than
ignoring one, so a misplaced key fails at check time instead of quietly doing
nothing. A type that crosses
an edge needs a stable name for pipeline files and error messages, given by
`node.RegisterType[T](name string) node.TypeID` from an `init` function, for
example `node.RegisterType[*types.Text]("Text")`.

A node whose work genuinely stops when its context ends can say so with
`node.AsStoppable(n)`, which `exec` does because it kills the process it
started. It buys a better report rather than a longer deadline: an abandoned
step that made the promise is reported as having broken it (ADR 0016).

Two rules an author must not break:

- **Payloads on edges are pointers.** `*Report`, never `Report`. An interface
  holding a pointer allocates nothing on an edge; one holding a struct
  allocates on every edge, which measured 31 ns and one allocation against
  13 ns and zero when the two were compared directly (ADR 0001).
- **Outputs are immutable.** Fan-out hands every dependent the same reference;
  nothing copies a payload. Producing a changed value means allocating a new
  one, never mutating the one just returned.

## Errors and retry

A node reports facts: a `Kind` and whether the failure is `Retryable`. A
workflow declares policy: `max_attempts` and `backoff` on a step. The engine
applies the policy to the facts; a node never decides how often it gets
retried.

| Kind | Meaning |
|---|---|
| `transient` | The same call might succeed if repeated: a timeout, a 503, lock contention. |
| `permanent` | Repeating the call fails the same way: a 404, a missing binary, a rejected credential. |
| `invalid_input` | The input the node received is not usable. A bug in the pipeline, not a condition in the world. |
| `cancelled` | The execution was cancelled or its deadline expired. |
| `internal` | The node or the engine broke. Recovered panics land here. |

A non-2xx HTTP status and a non-zero process exit code are data, not errors:
`http.request` returns a `*types.Response` with `Status` set to whatever the
server sent, and `exec` returns a `*types.CommandResult` with `ExitCode` set
to whatever the process returned. Whether a 404 or a failing exit code means
the workflow failed is a decision only the workflow can make, typically with a
`condition` step reading the field.

## Built-in nodes

| Capability | Input | Output | Notes |
|---|---|---|---|
| `value` | (none) | `Bytes`, `Text`, `Bool`, or `Int` | Constant from `with.type` and `with.value`. |
| `env.get` | (none) | `Text`, `Bytes`, `Bool`, or `Int` | Environment variable named by `with.name`, read at execution so `check` needs no secrets. |
| `text.format` | one `Text` per placeholder | `Text` | Interpolation the compiler checks: `with.template`'s `{{name}}` placeholders become input ports. |
| `json.decode` | `Bytes` | `JSONDocument` | Malformed input is `invalid_input`, not retryable. |
| `json.encode` | `JSONDocument` | `Bytes` | The inverse, so a pipeline can produce a payload and not only consume one. |
| `json.get` | `JSONDocument` | `Text`, `Bytes`, `Bool`, or `Int` | Reads `with.path` as the type `with.as` names; never coerces. |
| `condition` | `JSONDocument` | `Bool` | Tests a path with `equals` or `exists`; no branching in V0. |
| `assert.true` | `Bool` | `Bool` | Fails the run on a false verdict, which is how a check becomes an exit code. |
| `exec.command` | (none) | `Command` | Builds a command to run from `with.name`/`with.args`/`with.dir`/`with.timeout`. |
| `exec` | `Command` | `CommandResult` | Runs the process; a non-zero exit code is data, a failure to start it is not. |
| `exec.stdout` | `CommandResult` | `Bytes` | Extracts the output stream. |
| `exec.stderr` | `CommandResult` | `Bytes` | Extracts the error stream. |
| `exec.exit_code` | `CommandResult` | `Int` | Puts the exit code on an edge, which is what makes it data a workflow can read. |
| `http.build` | (none) | `HTTPRequest` | Builds a request from `with.method`/`with.url`/`with.headers`/`with.body`; the URL is validated at compile time. |
| `http.request` | `HTTPRequest` | `HTTPResponse` | Shared `http.Client`; a timeout is transient, a non-2xx status is data. Internal destinations are refused unless `with.allow_private` (ADR 0014). |
| `http.body` | `HTTPResponse` | `Bytes` | Extracts the body. |
| `http.status` | `HTTPResponse` | `Int` | Puts the status code on an edge, which is what makes it data a workflow can read. |
| `http.header` | `HTTPResponse` | `Text` | Reads one header by `with.name`; an absent one is an error unless `with.default` is set. |
| `http.from_url` | `Text` | `HTTPRequest` | A request whose URL comes from an edge; the URL is checked on arrival rather than at compile time. |
| `http.with_header` | `HTTPRequest`, `Text` | `HTTPRequest` | Sets `with.name` from an edge, producing a new request. |
| `http.with_body` | `HTTPRequest`, `Bytes` | `HTTPRequest` | Sets the body from an edge, producing a new request. |
| `http.assert_status` | `HTTPResponse` | `HTTPResponse` | Opt in to failing on a status outside `with.equals` or `with.min`/`with.max`. |

A node has one output, so a node producing several values bundles them into one
type. The extractors above are what put an individual field back on an edge;
without them the bundled values are unreachable.

Nothing interpolates. Where a value has to be built from data, a node builds it
and the graph shows it: `text.format` composes a string from typed ports, and
the `http.from_url`/`with_header`/`with_body` trio assembles a request from
edges. That keeps the convenience of `${{ ... }}` while the compiler still
checks every value's type and presence (ADR 0010).

There is no branching either. The assertions are how a computed fact becomes the
run's outcome: a false verdict fails the step, which stops the run and exits
non-zero, so a pipeline is usable from cron or CI where the exit code is the
whole interface. Since the assertions pass their input through, a step that
consumes one runs only when it held. See `examples/monitor.yaml`.

## Performance

Measured on an Apple M3 Pro, Go 1.26.5, darwin/arm64 (`docs/adr/0003-v0-baseline-performance.md`):

- A step costs about 491 ns of engine overhead on a 100-step chain, independent
  of the node's own work, or **98 ns with `--inline`**.
- The typed adapter that bridges a step's Go types to the plan is 11.65 ns per
  edge, zero allocations: under 3% of a step's cost.
- A 16 MiB payload fanned out to 32 consumers allocates 12.3 KB in total for
  the whole run. Fan-out shares one reference; the allocation figure does not
  grow with payload size.
- Compiling a 100-step pipeline takes 34 us, paid once per plan and never per
  run.

ADR 0003 records the figures as first measured. Two have moved since, both from
extracting the coordinator into a `scheduler` type: a run allocates two more
times (112 to 114, or 12 to 14 under `--inline`) because that type is one
allocation and its methods no longer share a closure frame, and it allocates 6
to 16% fewer bytes for the same reason. Per-step time did not change
measurably.

Most of a step's cost is the scheduler's goroutine handoff, which measures
268 ns on its own. `p6e run --inline` removes it for any step that is the only
one ready, taking a 100-step chain from 491 to **98 ns per step** and its
allocations from 114 to 14 per run. Fan-out is unaffected, since only the root of
a fan-out is ever solitary.

It is off by default, and the reason is worth knowing: an inlined step runs on
the coordinator goroutine, so while it runs nothing is left to abandon it. By
default `p6e` guarantees that `Run` returns within `AbandonAfter` once the
execution is cancelled or has failed, even if a node ignores its context; with
`--inline`, such a node wedges the run instead. Turn it on for pipelines whose
nodes you control. See ADR 0003, ADR 0004 and ADR 0008.

## Adding a node

Nodes are added in-tree: register a `node.Definition` in `internal/nodes` and it
is available to every pipeline, with no engine change of any kind. All 22
built-ins were written against exactly the interface described above.

Out-of-tree authoring is not possible yet, because every package is under
`internal/`. That is deliberate and temporary: three parts of the contract are
expected to change (multi-output ports, arity beyond two, interface-typed
compatibility), and exporting them first would mean breaking anyone who built on
them. ADR 0007 records the reasoning and the trigger for revisiting it.

## Adding a trigger

Triggers live in `internal/trigger` and are registered the same way nodes are,
but they implement a different contract, because they are a different thing: a
node is pulled once per run and returns a value, while a trigger is pushed by
the world, fires an unbounded number of times, and lives as long as the process.

A trigger declares what one event supplies, as named typed values. The compiler
matches those against the pipeline's `inputs`, which is the whole integration:
nothing else in the engine learns that triggers exist.

There are two contracts, chosen by who owns the resource:

- `SelfDriven` runs its own loop, as a schedule's timer does.
- `HTTPDriven` is driven by the daemon's shared listener. A webhook cannot own
  its socket, because one listener serves every webhook pipeline in the process
  and the daemon routes by claim.

A `Claim` is the process-wide resource a trigger needs to itself, such as
`POST /hooks/deploy`. Two served pipelines making the same claim are both
rejected, and `p6e check --dir` reports it.

**A webhook authenticates nothing unless you say so.** Give the trigger an
`auth` block and every event must carry a valid HMAC-SHA256 signature over the
raw body before any run starts:

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

The secret is named rather than inlined, so it stays out of the pipeline
directory and `p6e check` still needs no secrets: the block is validated at
compile time and the variable is read per request. A rejected event gets `401`
and nothing else, because telling a sender which half of the signature was wrong
tells an attacker which half to work on; the reason goes to the log. Without an
`auth` block the route is open, and the daemon says so at startup, naming every
open route. See ADR 0013.

## Running it as a service

```bash
make local                         # dev stack, ports published, creates .env
make logs
make down
```

For production, `make up` drives the prod overlay and is gated by
`make preflight`, which refuses to start on a missing `.env`, a placeholder
`APEX_DOMAIN`, or a pipeline directory that does not compile. It runs
`p6e check --dir` for you, so a route collision stops the deploy rather than the
daemon.

```bash
cp env.sample .env                 # set APEX_DOMAIN and P6E_PIPELINES
make up
```

The image is a static binary on alpine, about 8 MB, running as uid 1001. The
pipeline directory is mounted read-only at `/pipelines`: it is the unit of
deployment, and baking it into the image would mean rebuilding to edit a
pipeline.

Two ports, and the split matters. `8080` answers webhooks. `8081` serves
`/healthz`, `/readyz` and `/metrics`, and it defaults to loopback because it
describes every pipeline in the process. Every compose overlay publishes it to
`127.0.0.1` only; reaching it from a scraper is a deliberate act.

`docker-compose.yml` is a neutral base with no host ports or restart policy;
`docker-compose.local.yml` publishes ports and fails fast, and
`docker-compose.prod.yml` joins an existing Traefik on an external `proxy`
network, applies HSTS and baseline security headers at the edge, sets resource
limits, and refuses to start on a placeholder domain.

Validate a directory before deploying it, which is the same check the daemon
runs at load time and the reason `--dir` exists:

```bash
p6e check --dir /path/to/pipelines
```

## Status and non-goals

V0, under active development. The decisions behind it are in `docs/adr/`.
Deliberately absent: an expression or interpolation DSL, external module tiers,
distributed execution, and a visual editor.

`p6e serve` runs a directory of pipelines, each when its trigger fires. It is
deliberately small: interval schedules with no cron syntax, synchronous webhook
replies with no execution store, and no queue anywhere. A trigger is not a node
and not a step; it supplies the values a pipeline declares under `inputs`, which
is why the executor knows nothing about triggers and why a triggered pipeline
still runs by hand with `--input`. See ADR 0012.

**Persistence is deferred rather than refused.** Nothing survives a run today,
which is what keeps the daemon this small. Three things need that to change and
are worth doing together if any of them is: asynchronous webhook replies (`202`
plus an identifier), surviving a restart with work in flight, and run history.
ADR 0012 records what is already shaped to accommodate it.

There is also no dynamic `Any` type. One was allowed for in the design as an
escape hatch, and no V0 node needed it, so it was not built. Adding it later
would be adding an escape hatch to a working type system, which is a smaller
and better-understood change than removing one that everything came to depend
on.

## Documentation

- `docs/pipeline.md`: the pipeline file format in full, and a reference for
  every built-in node: its inputs, output, configuration, and errors.
- `docs/adr/0001-type-bridge.md`: why nodes exchange pointer payloads through
  an erased runtime adapter rather than reflection or compile-time fusion.
- `docs/adr/0002-input-binding.md`: why `needs` binds positionally to a node's
  input ports.
- `docs/adr/0005-named-input-binding.md`: the mapping form of `needs`, which
  binds by input port name.
- `docs/adr/0009-mandatory-named-binding.md`: why that mapping form is required
  when a node's input ports are not pairwise type-distinct.
- `docs/adr/0010-data-dependent-values.md`: how a pipeline builds strings and
  requests from data without an expression language.
- `docs/adr/0011-parameterized-execution.md`: why an input is a graph node, and
  what it costs the compile-time guarantee.
- `docs/adr/0012-triggered-pipelines-and-daemon-mode.md`: why a trigger supplies
  a run's inputs rather than being a node, and what a long-lived process adds.
- `docs/adr/0013-webhook-authentication.md`: why signature verification is a
  trigger concern rather than a node or a job for the proxy.
- `docs/adr/0014-outbound-destination-policy.md`: why a request built from data
  cannot reach inside the deployment, and why the check lives in the dialer.
- `docs/adr/0015-checked-url-as-a-type.md`: why a validated URL is a type, and
  why an authenticated event is deliberately not one.
- `docs/adr/0016-stoppable-nodes.md`: why a node may declare that it honours
  cancellation, and why that declaration is not allowed to make `--inline` safe
  by default.
- `docs/adr/0003-v0-baseline-performance.md`: the measurements behind the
  Performance section above.

## Build

```bash
make build   # compile bin/p6e, with version metadata
make test    # run tests
make bench   # engine overhead benchmarks
make race    # tests under the race detector
make audit   # vet + lint + vuln scan + coverage gate
make ci      # the full local gate: tidy + audit + race
make image   # build the container image
```

CI runs those same targets rather than restating their commands, so what passes
locally and what passes on a push cannot drift.

## Releasing

```bash
make release
```

It derives the next version from the Conventional Commits since the last `v*`
tag (`feat` minor, `fix` patch, `!` major, capped to minor while on 0.x), offers
it as an editable default, runs `make ci`, then tags and pushes. Pushing the tag
is what triggers the release workflow: goreleaser builds static, `-trimpath`,
version-stamped binaries for linux, macOS and Windows on amd64 and arm64,
attaches an SBOM, and signs the checksums with cosign.

Each archive carries the binary plus the LICENSE, the README, the format
reference and the examples, so an unpacked tarball is self-contained.

One thing it still needs that this repo does not have: an `origin` remote to
push to. `make release` refuses with a clear message rather than failing
somewhere further in.

## License

MIT. See `LICENSE`.

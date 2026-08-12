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

`p6e nodes` lists the registered capabilities a pipeline file can reference:

```
$ ./bin/p6e nodes
condition
exec
exec.command
http.body
http.build
http.request
json.decode
value
```

## How it works

A pipeline has two phases: compile, then run. Compiling turns a YAML file into
an `ExecutionPlan`: it resolves every `uses` name against the node registry,
checks that dependencies exist, detects cycles, binds each step's `needs` list
positionally to its node's declared input ports (ADR 0002), checks that the
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
with `node.Ok(v)` or `node.Fail(err)`. A failure is a `*node.NodeError`, never
a panic; it carries a `Kind` (`transient`, `permanent`, `invalid_input`,
`cancelled`, `internal`) and a `Retryable` flag, built with `node.Errf` or
`node.Wrap`.

`node.Definition{Name, New}` is what gets registered: `New(cfg node.Config)
(node.RuntimeNode, error)` runs once per step, at compile time, never during
execution, which is what lets configuration decoding and validation happen
ahead of the hot path. `node.Static(name, n)` is the shortcut for a node that
takes no configuration; a node that must reject a stray `with` block decodes
into an empty struct instead, the way `json.decode` does. A type that crosses
an edge needs a stable name for pipeline files and error messages, given by
`node.RegisterType[T](name string) node.TypeID` from an `init` function, for
example `node.RegisterType[*types.Text]("Text")`.

Two rules an author must not break:

- **Payloads on edges are pointers.** `*Report`, never `Report`. An interface
  holding a pointer allocates nothing on an edge; one holding a struct
  allocates on every edge, 31 ns and one allocation instead of 13 ns and zero
  (ADR 0001).
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
| `json.decode` | `Bytes` | `JSONDocument` | Malformed input is `invalid_input`, not retryable. |
| `condition` | `JSONDocument` | `Bool` | Tests a path with `equals` or `exists`; no branching in V0. |
| `exec.command` | (none) | `Command` | Builds a command to run from `with.name`/`with.args`/`with.dir`/`with.timeout`. |
| `exec` | `Command` | `CommandResult` | Runs the process; a non-zero exit code is data, a failure to start it is not. |
| `http.build` | (none) | `HTTPRequest` | Builds a request from `with.method`/`with.url`/`with.headers`/`with.body`; the URL is validated at compile time. |
| `http.request` | `HTTPRequest` | `HTTPResponse` | Shared `http.Client`; a timeout is transient, a non-2xx status is data. |
| `http.body` | `HTTPResponse` | `Bytes` | Extracts the body. |

## Performance

Measured on an Apple M3 Pro, Go 1.26.2, darwin/arm64 (`docs/adr/0003-v0-baseline-performance.md`):

- A step costs about 520 ns of engine overhead on a 100-step chain, independent
  of the node's own work.
- The typed adapter that bridges a step's Go types to the plan is 15.3 ns per
  edge, zero allocations: under 3% of a step's cost.
- A 16 MiB payload fanned out to 32 consumers allocates 11.9 KB in total for
  the whole run. Fan-out shares one reference; the allocation figure does not
  grow with payload size.

About 60% of a step's cost is the scheduler's goroutine handoff: the executor
spawns a goroutine per ready step and receives completion on a channel, and
that round trip alone measures 318 ns. The identified next optimization is
running a solitary ready step inline, on the coordinator goroutine, instead of
spawning one; it is not built yet, in favor of the current single-owner
executor design that is race-free by construction. See ADR 0003 for the full
measurement set and the reasoning.

## Status and non-goals

V0, under active development; see `PLAN.md`. Deliberately absent: an
expression or interpolation DSL, external module tiers, a scheduler, a
persistence layer, distributed execution, and a visual editor.

There is also no dynamic `Any` type. One was allowed for in the design as an
escape hatch, and no V0 node needed it, so it was not built. Adding it later
would be adding an escape hatch to a working type system, which is a smaller
and better-understood change than removing one that everything came to depend
on.

## Documentation

- `docs/adr/0001-type-bridge.md`: why nodes exchange pointer payloads through
  an erased runtime adapter rather than reflection or compile-time fusion.
- `docs/adr/0002-input-binding.md`: why `needs` binds positionally to a node's
  input ports.
- `docs/adr/0003-v0-baseline-performance.md`: the measurements behind the
  Performance section above.

## Build

```bash
make build   # compile bin/p6e
make test    # run tests
make bench   # engine overhead benchmarks
make race    # tests under the race detector
make audit   # vet + lint + vuln scan
```

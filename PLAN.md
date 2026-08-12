# p6e: Typed Low-Latency Pipeline Runtime in Go (V0 development plan)

## Context

Build a small, fast pipeline execution engine in Go from the handoff spec: workflows are versionable YAML files, compiled ahead of execution into a typed DAG plan, then run with minimal overhead (no JSON serialization between native Go nodes, no reflection on the hot path). The engine is a "compiler + runtime for pipelines", not an n8n clone. V0 scope is defined by the handoff's Definition of Done (section 26).

Decisions already made:
- Project: `~/code_perso/p6e/`, module `github.com/arhuman/p6e`, CLI binary `p6e` (`p6e check`, `p6e run`). The handoff calls the binary `pipe`; the project rename carries to the binary for coherence.
- The type-bridge spike winner is auto-picked from benchmark results and recorded as an ADR; no pause for review.
- New greenfield repo (existing `wuflow/` is unrelated). jj colocated, Conventional Commits, workspace-standard make targets.

## Package layout

Handoff section 27 keeps the domain packages at the repo root. We put them under
`internal/` instead: nothing is imported by third parties in V0, and `internal/`
enforces that until we deliberately decide a package is a public contract.

```
cmd/p6e/                     CLI (the only non-internal code)
internal/pipeline/           config.go, parser.go, compiler.go, plan.go
internal/node/               node.go, descriptor.go, registry.go, types.go, error.go, result.go, value.go
internal/runtime/            executor.go, scheduler.go, state.go
internal/nodes/              value/, exec/, http/, json/, condition/
internal/spike/              throwaway benchmark spike (step 2, removed after ADR)
examples/                    sample pipeline YAML files
docs/adr/                    committed ADRs
```

When out-of-tree node authoring becomes a real requirement (handoff section 32,
external modules), `internal/node` is the package to promote to the root: it is
the only one node authors need. That promotion is a deliberate later decision,
not a V0 concern.

## Ordered steps

### Step 1: Bootstrap
- Create `~/code_perso/p6e/`, `go mod init github.com/arhuman/p6e` (latest local Go), `jj git init --colocate`.
- Makefile with `build`, `test`, `tidy`, `audit` targets (apply the `10x-makefile` skill).
- Skeleton directories above, `docs/adr/0000-adr-template.md` copied from `~/docs/adr/`, minimal README and project CLAUDE.md, local `.claude/CHANGELOG.md`.
- Commit: `chore: bootstrap p6e project skeleton`.

### Step 2: Spike the type bridge (handoff sections 28-29, the hard problem)
In `internal/spike/`, prototype and benchmark the bridge between compile-time Go generics and dynamically loaded pipelines. Candidates:
1. Generics + erased runtime adapter: `Value{Type TypeID; ptr any}`, one type assertion per edge at execution.
2. Reflection-based invocation (`reflect.Value.Call`).
3. Typed function wrappers: per-edge closures generated at compile (plan-build) time, zero assertions at run time.

Benchmark a 3-node identity chain for each: ns/op and allocs/op for the A-completes-to-B-invoked handoff. Pick the fastest approach that keeps node authoring ergonomic (`NewTypedNode[I, O](fn)`), expected to be 1 or 3.
- Write `docs/adr/0001-type-bridge.md` with the numbers and the decision.
- Delete `internal/spike/` once the winning shape is ported into `internal/node/`.

### Step 3: Core node contract (`internal/node/`)
- `TypeID` plus a type registry: `RegisterType[T](id)` and `TypeOf[T]()`, giving every Go type used on a port a runtime identity.
- `Value`: typed reference wrapper (winning spike shape), no serialization.
- `Result[T]`, `ResultMeta{Duration, Attempt}`, `NodeError{Code, Kind, Message, Retryable, Cause}`, `ErrorKind` constants (transient, permanent, invalid_input, cancelled, internal).
- `PortDescriptor`, `NodeDescriptor` (name, inputs, outputs).
- `RuntimeNode` interface (`Descriptor()`, `Execute(ctx, ExecutionContext, Value) ResultValue`) and the `NewTypedNode[I, O]` adapter so authors write typed functions and erasure happens once at the boundary.
- `ExecutionContext{WorkflowID, ExecutionID, StepID, Attempt}` kept separate from business input; cancellation via `context.Context`.
- Unit tests for the adapter, error normalization, and type identity.

### Step 4: Node registry (`internal/node/registry.go`)
- `Registry` with `Register(def)` / `Resolve(name)`. A `NodeDefinition` bundles the descriptor, a typed config struct decoder (decode `with:` once at compile time, validate, fail early), and the executable implementation. Nodes are stateless per workflow; infrastructure state (HTTP pools) lives in the definition's long-lived implementation.

### Step 5: Config parsing (`internal/pipeline/config.go`, `parser.go`)
- YAML schema v1: `version`, `steps.<id>.{uses, with, needs, retry}`. Strict decoding (unknown fields are errors). No interpolation syntax in V0.
- Input binding rule (pin it now to avoid ambiguity): `needs` is an ordered list bound positionally to the node's declared input ports; a single-input node with one dependency needs no extra syntax; count/arity mismatch is a compile error. Record as `docs/adr/0002-input-binding.md`.

### Step 6: Compiler (`internal/pipeline/compiler.go`, `plan.go`)
Performs, in order: parse, structural validation, node resolution (unknown node error), missing dependency detection, cycle detection (Kahn), edge type-checking with the handoff's error format ("step X input expects T1 but step Y produces T2"), per-step config decoding/validation, then emits `ExecutionPlan`:
- `CompiledStep`: resolved implementation, decoded typed config, precomputed dependency count, dependents adjacency (indices, not names), result-slot index, retry policy.
- Nothing is resolved by name at run time.
- Unit tests: one test per failure class (unknown node, missing dep, cycle, type mismatch, bad config, arity mismatch) plus a golden success case.

### Step 7: Executor (`internal/runtime/`)
- States: pending, ready, running, succeeded, failed, cancelled, skipped.
- Dependency-counter scheduling from the precomputed plan: roots ready, execute ready steps concurrently (goroutine per ready step in V0, no premature pooling), store result reference in a per-execution slot array, decrement dependents' counters, promote to ready.
- Outputs treated as immutable; fan-out shares the same `Value` reference, no copies.
- Failure semantics: a failed step (after policy) fails the execution and marks transitively dependent steps skipped; independent branches still finish or are cancelled via context (pin: cancel outstanding work on first hard failure).
- Panic recovery at the node boundary normalized to `ErrorKind` internal.
- Unit tests: linear chain, fan-out runs concurrently (assert with barriers), fan-in, failure skip-propagation, cancellation, shared node used by two concurrent executions.

### Step 8: Retry policy (engine-side)
- Per-step `retry: {max_attempts, backoff}` in YAML, compiled into the plan. The engine retries only when `NodeError.Retryable` (facts from the node, policy from the workflow, application by the engine). Attempt count flows into `ExecutionContext` and `ResultMeta`.
- Tests with a fake node failing N times with transient vs permanent errors.

### Step 9: Built-in nodes (`internal/nodes/`)
Only the five from handoff section 24, each a normal `NewTypedNode` registration with its own typed config and tests:
- `value`: typed constant from config (type name + literal), mainly for tests and examples.
- `exec`: `Command{Name, Args}` to `CommandResult{ExitCode, Stdout, Stderr}`; nonzero exit is a value, process-start failure is a NodeError.
- `http.request`: `HTTPRequest` to `HTTPResponse`, shared `http.Client` (connection pooling = allowed infrastructure state); timeouts map to transient errors.
- `json.decode`: `Bytes` to `JSONDocument`.
- `condition`: produces `Bool`; no branching semantics in V0.
- Domain types registered in the type registry; HTTP tests via `httptest`.

### Step 10: CLI (`cmd/p6e`)
- `p6e check pipeline.yaml`: full compile, human-readable compile errors, exit code 1 on failure.
- `p6e run pipeline.yaml`: compile then execute, print per-step results/errors, nonzero exit on failure.
- Two or three `examples/*.yaml` pipelines exercised by an integration test (value -> json.decode chain; exec chain; one intentionally broken example asserting `check` failures).

### Step 11: Benchmarks (handoff section 30)
Engine overhead measured separately from node work, in `internal/runtime/bench_test.go`:
- identity-node chain (source, 3 noops, sink): the key metric is engine latency between node A completion and node B invocation;
- sequential 100-node pipeline; 100-way fan-out; fan-in; concurrent executions of the same plan; large `[]byte` payload fan-out (asserting zero copies).
- Record baseline numbers in the ADR 0001 addendum or `docs/adr/0003-v0-baseline-performance.md`.

### Step 12: Wrap-up
- README (architecture overview, quickstart, node-author guide); project CLAUDE.md with build/test commands.
- Verify every Definition of Done item from handoff section 26; `make test` and `make audit` clean.
- Atomic jj commits throughout (roughly one per step), Conventional Commits, changelog entries in local `.claude/CHANGELOG.md`.

## Non-goals honored

No interpolation DSL, no external module tiers (Tier 1/2), no warm/cold management, no scheduler, no persistence, no visual editor. `Any` type exists as an escape hatch only if a V0 node forces it; otherwise omitted.

## Verification

- `make test` covers compiler failure classes, executor concurrency/failure semantics, retry policy, and each built-in node.
- `p6e check examples/broken.yaml` reports each static error class; `p6e check` then `p6e run` succeed on the valid examples.
- `go test -bench . ./runtime/` produces the overhead numbers; confirm the engine adds no allocations on the identity-chain hot path beyond result bookkeeping, and no JSON anywhere between native nodes (grep: no `encoding/json` import in `internal/runtime/`).
- Concurrency safety: `go test -race ./...`.

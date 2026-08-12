# p6e

A typed, low-latency pipeline runtime in Go.

p6e compiles a versionable YAML pipeline into a statically type-checked execution
DAG, then runs it with minimal overhead. Native Go nodes exchange values by
reference: no JSON, no IPC, no reflection on the hot path.

It is a compiler and runtime for pipelines, not a workflow automation product.

```bash
p6e check pipeline.yaml   # compile and validate without running
p6e run   pipeline.yaml   # compile, then execute
```

## Status

V0 in development. See `PLAN.md`.

## Build

```bash
make build   # compile bin/p6e
make test    # run tests
make bench   # engine overhead benchmarks
make audit   # vet + lint + vuln scan
```

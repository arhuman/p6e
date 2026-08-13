# Contributing

## Getting set up

```bash
make tools    # install the pinned golangci-lint and govulncheck
make build    # compile bin/p6e
make test     # short tests with coverage
```

Go 1.26 or newer. The only dependency is `gopkg.in/yaml.v3`; please keep it that
way unless there is no reasonable alternative, and say why in the PR.

## The gate

```bash
make ci       # tidy + audit + race: run this before opening a PR
```

`make audit` is `go mod verify`, `go vet`, `golangci-lint`, `govulncheck`, and a
coverage gate at `COVER_MIN` (85). CI runs the same make targets rather than
restating their commands, so a green local run means a green CI run.

| Target | What it does |
|---|---|
| `make test` | short tests with coverage |
| `make race` | the full suite under the race detector |
| `make bench` | engine overhead benchmarks |
| `make cover` | coverage profile plus the gate |
| `make local` | the dev stack in Docker, ports published |
| `make up` | the production stack, gated by `make preflight` |

Coverage is a ratchet. Raise `COVER_MIN` as it improves; never lower it to make
a red build pass.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/), enforced by CI on
every PR commit:

```
feat: add a trigger contract for pipelines that a process serves
fix: release the shared slot when a step is abandoned
docs: record why a trigger supplies inputs rather than being a node
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`,
`ci`, `chore`, `revert`. A `!` or a `BREAKING CHANGE` body bumps the minor
version while the project is on 0.x.

Keep commits atomic: one change per commit, each building and passing its own
tests. The version control is [jj](https://jj-vcs.github.io/) colocated with
git, so either works.

## What a good change looks like

This project has strong opinions, recorded in `docs/adr/`. Read the ones that
touch what you are changing before you change it. The invariants in `CLAUDE.md`
are not style preferences: each of them has an ADR explaining what breaks
without it.

Three that catch most newcomers:

- **Anything checkable before execution is checked by the compiler.** If a
  change moves a check to run time, it needs an argument for why it cannot
  happen at compile time.
- **No expression or interpolation DSL.** It has been proposed and refused
  repeatedly (ADR 0010). Values are built by nodes with typed ports.
- **Tests define the contract.** Changing an existing test's expectations is a
  change to what the engine promises, so say so explicitly in the PR rather
  than folding it into a refactor.

New nodes are added in-tree under `internal/nodes/` and need no engine change;
the README's "Adding a node" section is the guide. Triggers work the same way
under `internal/trigger/`.

## Docs

Anything that changes behaviour changes `docs/pipeline.md` in the same PR.
Anything that changes a decision gets an ADR under `docs/adr/`, numbered
sequentially, using `docs/adr/0000-adr-template.md`.

`CHANGELOG.md` at the root is public-facing and populated from tagged releases;
you do not normally edit it by hand.

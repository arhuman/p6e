# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the project is on 0.x, a breaking change bumps the minor version.

## [Unreleased]

### Added

- Typed pipeline engine: a YAML file compiles to a statically type-checked DAG,
  then runs. `p6e check` rejects a mismatched edge before anything executes.
- `p6e run` executes a pipeline once and exits non-zero on failure, so it works
  from cron or CI where the exit code is the whole interface.
- Pipeline inputs: `inputs:` declares typed values the run supplies, so one
  compiled plan serves many runs. `p6e run --input NAME=VALUE`, or `NAME=@FILE`.
- Triggered pipelines and `p6e serve <dir>`: a daemon runs every pipeline in a
  directory that declares a trigger, each when its trigger fires.
  `trigger.webhook` runs one per matching HTTP request and answers the caller;
  `trigger.schedule` runs one per interval.
- `p6e check --dir <dir>` compiles a whole directory and reports two pipelines
  claiming one route, which no single-file check can see.
- Admin endpoints on a separate loopback listener: `/healthz`, `/readyz` and
  `/metrics` in Prometheus text format.
- 22 built-in nodes: constants, environment variables, string templating, JSON
  decode/encode/extract, conditions, assertions, local processes, and HTTP
  requests with composable builders.
- `p6e nodes`, `p6e triggers` and `p6e version`.
- Container image and a compose stack: static binary on alpine, non-root, with
  the pipeline directory mounted read-only.

### Notes

This is pre-1.0 and every release is a prerelease. Every package is under
`internal/`, so out-of-tree node authoring is not possible yet: parts of the
node contract are still expected to change.

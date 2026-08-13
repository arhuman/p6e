# Security

## Supported versions

p6e is pre-1.0 and every release is a prerelease. Only the latest tag receives
fixes; there are no maintenance branches for older ones.

| Version | Supported |
|---------|-----------|
| latest `v0.x` tag | yes |
| any earlier tag | no, upgrade |

## Reporting a vulnerability

Report privately, not in a public issue. Use GitHub's **Report a vulnerability**
button under the Security tab, or email <arhuman@gmail.com>.

Please include what you need to reproduce it: the pipeline YAML, the trigger
that fired it, and what you expected to happen instead. A first response should
take a few days. There is no bounty.

## What is in scope

The parts of p6e that meet input you do not control:

- **The webhook listener.** Request bodies are attacker-controlled. They are
  bounded by `max_body` and read before a run starts, but anything reachable
  through them is in scope.
- **The compiler.** A pipeline file is a trusted input in normal use, but a
  crash or an unbounded allocation while compiling one is still a bug.
- **The admin listener** (`/healthz`, `/readyz`, `/metrics`). It defaults to
  loopback precisely because it describes every pipeline in the process. Report
  anything that exposes more than it should.

## What is not

- **A pipeline that does something dangerous because it was told to.** The
  `exec` node runs local processes and `http.request` calls out, by design.
  Anyone who can write a pipeline file already has the daemon's privileges, so
  treat that directory as you would a crontab.
- **Serving a pipeline directory you do not control.** There is no sandbox
  between pipelines: they share a process and a step budget.
- **A node that ignores its context.** It leaks a goroutine, the engine reports
  it, and repeated offences quarantine the pipeline. That is a documented
  limitation of Go's scheduler (ADR 0004), not a vulnerability.

## Hardening notes

- Put the webhook listener behind a reverse proxy that terminates TLS. The
  production compose overlay does this, with HSTS and baseline security headers
  at the Traefik edge.
- Keep `--admin-listen` on loopback, or behind the same proxy with
  authentication. Never publish it beside the webhook port.
- `p6e check --dir` before deploying a pipeline directory. `make preflight`
  runs it for you and refuses to start prod if it fails.

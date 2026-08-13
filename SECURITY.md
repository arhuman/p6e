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
- **Outbound calls built from data.** `http.from_url` takes its URL off an
  edge, so a webhook body can choose where the daemon connects. Internal
  destinations are refused by default (ADR 0014); a way around that policy is
  in scope.
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

### Webhooks authenticate nothing unless you configure them to

This is the single most important line in this document. A `trigger.webhook`
with no `auth` block runs its pipeline for **anyone who can reach the
listener**. Since the `exec` node runs local processes, an open route in front
of a pipeline that shells out is remote process execution.

Give the trigger an `auth` block, which verifies an HMAC-SHA256 signature over
the raw body before any run starts:

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
```

The secret is named rather than inlined, so it stays out of the pipeline
directory and `p6e check` still needs no secrets. See `docs/pipeline.md` and
ADR 0013.

The daemon logs a warning at startup naming every route that authenticates
nothing. Treat it as a finding, not as noise.

### The rest

- Put the webhook listener behind a reverse proxy that terminates TLS. The
  production compose overlay does this, with HSTS and baseline security headers
  at the Traefik edge. Edge authentication and signature verification answer
  different questions, so do both: a proxy cannot verify a sender's signature
  scheme on the daemon's behalf, and a signature does not give you TLS.
- Keep `allow_private` off unless a step is meant to reach inside the
  deployment. It is what stops a URL that arrived on an edge from reaching cloud
  metadata, your internal services, or p6e's own admin listener (ADR 0014).
- Keep `--admin-listen` on loopback, or behind the same proxy with
  authentication. Never publish it beside the webhook port.
- `p6e check --dir` before deploying a pipeline directory. `make preflight`
  runs it for you and refuses to start prod if it fails.

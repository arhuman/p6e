# 13. Webhook authentication as a trigger concern

Date: 2026-08-13

## Status

Accepted

## Context

ADR 0012 gave p6e a daemon and a `trigger.webhook`. A served webhook pipeline
runs whenever a matching request arrives, and until now nothing between the
socket and the run checked who sent it. `SECURITY.md` declares the webhook
listener in scope and calls request bodies attacker-controlled, but said nothing
about the listener itself being open, and `docker-compose.prod.yml` routes port
8080 through Traefik to the public internet with no authenticating middleware.

The consequence was worse than an open endpoint, because of what a pipeline can
do. The `exec` node runs local processes. A pipeline containing one, served on
an open route, is remote process execution for anyone who can guess the path.

There was also a capability gap that no deployment trick closes. GitHub, Stripe
and most of the ecosystem authenticate a webhook by signing the raw body with a
shared secret and sending the digest in a header. Verifying that requires the
raw bytes at the moment they arrive. A proxy in front of the daemon can require
a bearer token or mTLS, but it cannot verify a GitHub signature on the
daemon's behalf without reimplementing the scheme. So p6e could not consume the
most common kind of webhook there is, correctly, at all.

Three shapes were considered.

**Authenticate at the proxy only.** Cheapest, and it stays true to the daemon
being small. It fails the capability gap above: signature schemes are per
sender, and pushing them to the proxy means every operator reimplements one.

**A node that verifies.** Consistent with "everything is a node", and wrong for
the same reason a trigger is not a node (ADR 0012): the check must happen before
a run starts, and a node runs inside one. An authenticated pipeline would be one
whose first step happens to be a verify step, with nothing preventing a pipeline
from forgetting it.

**A trigger concern, verified before the run.** Chosen.

## Decision

`trigger.webhook` accepts an optional `auth` block. When present, every event
must carry a valid signature or no run starts.

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

Specifics that follow from the context above:

- **The secret is named, not inlined.** A pipeline directory is deployed and
  read like a crontab, so it is the wrong place for a credential. Naming an
  environment variable is the same bargain `env.get` makes, and it preserves the
  property that `p6e check` needs no secrets: the block is validated at compile
  time, the variable is read per request.
- **Verification happens in `Values`**, after the `max_body` cap and before
  anything else. The signature covers the raw body, so the body must be read
  first; nothing beyond reading it should happen for an event that turns out not
  to be authentic.
- **Every rejection reads identically to the caller**: `401`, body
  `unauthorized`. Distinguishing a missing header from a malformed prefix from a
  wrong digest tells an attacker which half of the problem to work on. The
  specific reason goes to the daemon's log, for the operator entitled to it.
- **Comparison is constant time** (`hmac.Equal`), so the expected digest is not
  leaked a byte at a time to a caller willing to measure.
- **An unset `secret_env` is `500`, not `401`.** A misconfigured daemon is not
  an unauthorized caller, and conflating them sends an operator hunting a
  signature bug that is really an unset variable.
- **`Authenticating` is a separate optional interface**, not a method on
  `HTTPDriven`. A trigger that cannot authenticate is a coherent thing to write,
  and forcing it to answer would force it to lie. The daemon consults it to warn
  at startup, naming every open route.

Only `hmac-sha256` is implemented. It is what the ecosystem uses, and adding a
second scheme later is additive.

## Consequences

**Authentication is still off by default, and that is the honest position.** A
webhook with no `auth` block is open exactly as before. Defaulting it on is not
possible: there is no secret to verify against until an operator supplies one,
and a default that fails every request would break every existing pipeline. What
changed is that the exposure is now sayable, configurable, and said out loud at
startup rather than discovered.

Depth rather than replacement: the proxy guidance in `SECURITY.md` stays. Edge
authentication and signature verification answer different questions, and the
recommendation is both.

The daemon learns one thing about triggers it did not know before, which is
whether they authenticate. That is a small widening of the interface ADR 0012
kept deliberately narrow, and it is justified because the alternative is an
operator learning the same fact from an incident.

The 401 path required the request-rejection branch in `daemon.handle` to stop
flattening every `Values` error into `400`, and to use the same
code-to-status mapping (`statusFor`) that a failure during a run already used.
That is a consolidation the handler wanted anyway.

`p6e check` still needs no secrets, so CI validation of a pipeline directory is
unaffected. A pipeline whose `secret_env` is unset in the checking environment
compiles; it fails at request time, with a message naming the variable.

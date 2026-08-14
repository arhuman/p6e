# 15. A checked URL is a type, an authenticated event is not

Date: 2026-08-14

## Status

Accepted

## Context

ADR 0014 gave `http.request` a destination policy, and ADR 0013 gave
`trigger.webhook` signature verification. Both closed real holes. Both left the
same structural weakness behind: the property they establish is held by
convention rather than by the type system, so the next node or trigger can omit
it and nothing says so.

For URLs the convention had two holders. `validateURL` was called by
`http.build` at compile time and by `http.from_url` at execution, and
`types.Request.URL` was a bare `string` that any code could fill. Four nodes
construct or copy a `Request` today (`http.build`, `http.from_url`,
`http.with_header`, `http.with_body`) and ADR 0007 commits to more arriving
in-tree. Every one of them is a place the check can be forgotten, and the
consequence is not cosmetic: `http.from_url` takes its URL off an edge that can
carry a webhook body, so the check is the difference between a call the pipeline
author chose and one an attacker did.

The same question was asked of the inbound side, and answered differently. See
Decision.

## Decision

### A checked URL is a type

`types.CheckedURL` wraps an unexported string. The only way to a non-empty one
is `types.NewCheckedURL`, which performs the parse, scheme and host checks that
`validateURL` used to. `types.Request.URL` is a `CheckedURL`, so a `Request`
cannot exist unless its target went through that constructor.

This is "parse, don't validate": the check happens once, at the boundary, and
produces a value that carries the proof. Downstream code stops asking whether
the URL is usable because it cannot hold one that is not.

Two consequences worth naming:

- **`validateURL` is gone.** There is no second copy to drift.
- **The scheme re-check in `build` stays**, with a changed reason. It is no
  longer a validation; it is the guard on the one case a value type cannot close
  in Go, the zero `CheckedURL`, because `types.CheckedURL{}` is always a legal
  composite literal. The zero value is the empty URL rather than a plausible
  one, so it fails loudly.

`CheckedURL` checks the **shape** of a URL, never where it points. The
destination policy stays in the dialer, on the address actually being connected
to, because a hostname can resolve to one address at check time and another at
connect time (ADR 0014). The two are complementary and neither replaces the
other.

### An authenticated event is not a type

The obvious symmetry would be an `AuthenticatedEvent` that `fire` alone accepts,
making an unauthenticated run unrepresentable. It was considered and rejected.

The reason is not effort, it is that the invariant does not exist. **An
unauthenticated event is a deliberately legal state in V0**: a webhook with no
`auth` block is open by design (ADR 0013), because there is no secret to verify
against until an operator supplies one. A type that made unauthenticated events
unrepresentable would forbid the documented default. A type that permitted them
would carry no invariant at all, and would be a wrapper whose only content is a
boolean.

The smallest alternative, which is what exists: `trigger.Authenticating`, an
optional interface a trigger implements to report whether it verifies anything.
It is sufficient because it already forces the question to be answered at the
type level, a trigger that does not implement it is treated as unauthenticated,
and the daemon names every open route in a warning at startup. The only thing a
per-event type would add is per-event granularity, and no trigger authenticates
some of its events and not others; one that did would be the finding.

Revisit this if a trigger ever needs to accept both authenticated and
anonymous events on one route, which is the case where per-event provenance
starts carrying information the interface cannot.

## Consequences

The URL check moved from run time to construction, so three failures that used
to surface inside `http.request` (a malformed URL, an unsupported scheme, a
missing scheme) are now impossible to hand it. Their tests moved with them, from
`TestUnusableRequestsArePermanent` to `TestCheckedURLRejectsUnusableURLs`. The
coverage is the same; the boundary is earlier.

Every test that built a `Request` from a raw string now goes through a `checked`
helper. That is not incidental cost: those tests were reaching past the
constructor, which is exactly the habit the type removes.

`internal/nodes/types` now holds behaviour rather than only data. That is a
change in what the package is, and it is the right home: the check belongs to
the type, and putting it in `httpnode` would mean any future non-HTTP producer
of a `Request` importing an HTTP package to build one.

The zero value remains constructible, so this is a strong convention rather than
a proof. Go offers nothing better for a value type; the alternative is an
interface with an unexported method, which costs an allocation on every edge and
is the thing ADR 0001 exists to avoid.

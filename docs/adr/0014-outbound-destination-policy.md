# 14. Outbound destination policy in the dialer

Date: 2026-08-13

## Status

Accepted

## Context

ADR 0010 introduced `http.from_url`, which builds a request whose URL arrives on
an edge rather than from a `with` block. That was the right call for its own
problem: it is how a pipeline builds a request from data without an expression
language. It also opened one that ADR 0010 did not weigh.

Combined with ADR 0012's `trigger.webhook`, the shipped node set expresses a
complete chain from an untrusted request body to an outbound call:

```
trigger.webhook -> json.decode -> json.get (as: Text) -> http.from_url -> http.request
```

Every node in it is a built-in. The URL is chosen by whoever sent the event, and
until now the only check was `validateURL`: that the string parses, uses `http`
or `https`, and has a non-empty host. Nothing about *where* it pointed.

That makes the daemon a confused deputy. It sits inside a network the caller
cannot reach and will fetch whatever it is told to: `169.254.169.254` for cloud
credentials, an internal service, or p6e's own admin listener on
`127.0.0.1:8081`, which describes every pipeline in the process. ADR 0012
deliberately put the admin surface on loopback so it would not be exposed by
accident, and an unguarded outbound call reaches around that.

A second, quieter hole: `http.build` validates its URL at compile time, which is
one of the nicer properties in the engine. The shared `http.Client` set no
`CheckRedirect`, so Go's default of following up to ten redirects applied. A
compile-time-validated call to a trusted host could be redirected anywhere,
which means the compile-time check was not load-bearing.

Where to enforce was the real question.

**On the URL string, in `validateURL`.** The obvious place, and wrong. Checking
a hostname means resolving it, and between that resolution and the connect that
follows, a second DNS answer can return a different address. Checking the string
is a check with a window in it.

**On the resolved address, in the dialer.** `net.Dialer.Control` runs after
resolution and immediately before connect, on the address actually being dialed.
No window. It also covers every redirect hop for free, since each hop dials
through the same transport.

## Decision

`http.request` refuses internal destinations by default, enforced in
`net.Dialer.Control`.

Refused: loopback, link-local (unicast and multicast), private and unique-local,
unspecified, and multicast. Permitted: everything else. A public address
operated by the caller is not the threat this closes, and pretending otherwise
would mean an allow-list of the entire internet.

A step opts out with `allow_private: true`, which is a decision about one step
rather than about the process, and reads in the pipeline file as what it is.

There are exactly two package-level transports, one per policy. A transport per
step would give each its own connection pool and reconnect constantly, defeating
the reuse ADR 0003 measured; a single transport cannot carry two policies,
because the policy lives in its dialer.

`CheckRedirect` is set: redirects are capped at ten (matching net/http's own
default, which setting `CheckRedirect` otherwise replaces) and re-checked for
scheme. The address policy needs no repeating there, since each hop dials
through the same transport.

A refused destination surfaces as `transport`, which is retryable. The dial
genuinely failed, and a pipeline pointed at a name whose resolution is being
fixed should get its retries.

## Consequences

The threat model in `SECURITY.md` is unchanged in spirit and now enforced: a
pipeline author who deliberately writes `allow_private: true` and points a step
at an internal service is still out of scope, exactly as "a pipeline that does
something dangerous because it was told to" always was. What is now closed is
the case where the *caller*, not the author, chooses the destination.

This is a breaking change for any pipeline calling a service inside its own
deployment, which must now say `allow_private: true`. That is the intended
cost. The failure is loud and names both the address and the missing option, and
the alternative, defaulting to permissive, is how the hole existed.

It also changed the existing `http.request` tests, which reach `httptest`
servers on loopback and now opt in explicitly. That is not incidental: those
tests reach inside the deployment, and now they say so.

The check is per connection rather than per request, so a pooled connection to
an allowed address is not re-checked on reuse. That is correct, since the peer
of an established connection does not change.

What this does not close: a hostname resolving to a public address that proxies
inward, and any internal service on a public IP. Both are outside what an
address policy can see. `http.build`'s compile-time URL check remains the
stronger guarantee for a URL that is known while writing the pipeline, and this
policy is what makes that check hold once redirects are involved.

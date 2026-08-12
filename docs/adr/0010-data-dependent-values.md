# 10. Building values from data without an expression language

Date: 2026-08-12

## Status

Accepted

## Context

Until now a pipeline could consume data but not build anything from it. Every
request was fixed when the pipeline was written: `http.build` is a source whose
URL, headers and body come from a static `with` block, so no call could depend
on what an earlier step produced. A multi-model review of what real pipelines
need put this first: three of three reviewers independently named a
data-dependent HTTP request as the largest gap, ahead of branching and
iteration.

The obvious solution is the one every comparable tool reaches for, and the one
this engine has ruled out from the start:

```yaml
url: "https://api.github.com/repos/${{ steps.repo.owner }}/releases"
```

Interpolation in a `with` block means a expression evaluator, a run-time
resolution step, and type checking that can only happen once the value exists.
That is precisely the property `p6e check` exists to provide, so the question is
not whether to add an expression language but what to do instead.

Two things had to be answered: how a string gets built from data, and how a
request gets assembled from one.

## Decision

**Values are built by nodes with typed input ports, never by syntax inside a
`with` block.**

### Strings: ports derived from the template

`text.format` parses its template at compile time and turns each placeholder
into a named input port:

```yaml
url:
  uses: text.format
  with:
    template: "https://api.github.com/repos/{{owner}}/{{name}}/releases"
  needs:
    owner: repo_owner
    name: repo_name
```

The template is still written as interpolation, which is what makes it readable,
but the placeholders are ports rather than an expression. Everything a DSL would
have deferred to run time is therefore an ordinary compile-time check:

- a placeholder with nothing bound to it is an unbound-input error,
- a `needs` key naming a placeholder the template does not contain is an
  unknown-port error,
- every bound value is type checked as a `Text` like any other edge.

Because the ports all share a type, ADR 0009 requires the mapping form for any
template with two or more placeholders. That was not designed for this node, but
it is exactly right for it: a template's argument order is not something a
reader should have to reconstruct.

Supporting this needed one addition to the node contract, `NewTypedNodeN`: an
adapter for a node whose arity comes from its configuration. It is the general
form of `NewTypedNode2` and keeps type erasure in the one place ADR 0001 puts it.

### Requests: composition, not a second builder

A request with a computed URL starts at `http.from_url` and is refined by
`http.with_header` and `http.with_body`, each `(HTTPRequest, X) -> HTTPRequest`.

The rejected alternative was making `http.build`'s URL overridable, which would
have forced a placeholder URL into every dynamic pipeline:

```yaml
request:
  uses: http.build
  with: {url: https://placeholder.invalid}   # never used, and a trap if with_url is forgotten
```

A separate source states the intent in the type signature instead: this
request's URL comes from an edge.

### Extraction: the declared type is the output type

`json.get` and `env.get` name the type they produce in their `with` block, and
that becomes the step's output type. This is the mechanism the `value` node
already used, and it is what makes extraction statically typed without
structural types: the pipeline declares the type it expects, and the compiler
checks every downstream use against it. Conversion never coerces.

## Consequences

### Positive

- Interpolation, in the form authors actually want, is available and is checked
  by the compiler rather than at run time.
- Nothing in the engine changed. No expression evaluator, no run-time resolution
  step, no change to the plan format or the executor.
- The graph still describes the whole computation. A URL built from two fields
  is three visible steps, not a string that hides them.
- `env.get` reads at execution rather than compile time, so `p6e check` runs on
  a machine without secrets and one plan serves several environments.

### Negative

- **A computed URL cannot be validated at compile time.** `http.build` checks
  its URL when the pipeline compiles; `http.from_url` cannot check one until it
  arrives. Static type checking is unaffected, and the check still happens at
  the step that produced the bad URL rather than downstream, but this is a real
  reduction in what `p6e check` proves, taken only by pipelines that opt in.
- **Verbosity.** Building one URL from two fields is three steps. This is the
  standing trade of the whole design, and it is more visible here than anywhere
  else so far.
- `text.format` is a template language, however small. The line held is that it
  has no operators, no conditionals and no field access: a placeholder names a
  port and nothing else. That line will be under pressure the first time someone
  wants `{{user.name}}`, and the answer is a `json.get` step.

### Risks

- Values with no optional type keep needing a `default`, and three nodes now
  carry one (`json.get`, `env.get`, `http.header`) with the same rule: a default
  covers absence, never a value of the wrong type. If a fourth appears, the
  pattern is worth extracting rather than restating.

## Alternatives Considered

### An expression DSL in `with` blocks

Rejected, as it has been from the start. It moves type checking to run time,
which is the property the engine exists to provide. Every reviewer agreed
independently, including the two that argued hardest for other relaxations.

### `text.concat` instead of a template

Simpler, and proposed as the safer option on the grounds that templating becomes
a DSL in disguise. Rejected because concatenation of five fragments is unreadable
at exactly the point where a URL needs to be read carefully, and because the
template form is what generates good port names.

### A generic projector reading any field path into an `Any`

Rejected. Nominal `TypeID` cannot check field paths, so this would have produced
a value the compiler knows nothing about, and `Any` spreads.

## References

- ADR 0001 for the type bridge that `NewTypedNodeN` extends.
- ADR 0009, whose duplicate-type rule governs `text.format`'s ports.
- `.claude/doc/useful-pipeline-nodes.md`, the multi-model evaluation that ranked
  these capabilities. Local only, not committed.

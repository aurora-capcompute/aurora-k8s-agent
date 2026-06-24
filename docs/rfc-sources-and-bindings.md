# RFC: Sources & Bindings

Status: accepted, partially implemented.

## Problem

Today the agent is wired around two near-duplicate "channels" (Telegram, Slack),
each selected by a single `AURORA_CHANNEL` value. The *caller* — who is invoking
the agent, over which transport, and with which manifest — is implicit. We want
to:

1. make the caller a first-class concept that can be reasoned about and extended;
2. run more than one caller at once against a single runtime;
3. state a manifest as a named definition and **bind** it to a specific
   `(source × subject)` rather than copying it per user;
4. leave room for non-interactive callers (e.g. a Kubernetes informer that opens
   a run when a resource changes) and, eventually, CRD-driven configuration.

## Model

A **Source** is a first-class caller: it owns a transport, identifies a
**subject** (a user) within a **scope** (a chat/channel), and drives runs against
the shared runtime. Interactive sources (Telegram, Slack) render run status and
approvals back to the scope; non-interactive sources (informer) translate
external events into runs.

A **binding** ties a named **manifest** to a set of `(source, subject, scope)`
tuples. Authorization and manifest selection are one lookup: "is this subject,
in this scope, on this source, bound to a manifest?".

```
Source  ── identifies ──▶ subject + scope
   │                          │
   └── binding(source, subject, scope) ──▶ manifest (named) ──▶ run
```

## Increments

1. **Source abstraction (extraction).** Define `source.Source` (`Kind()` +
   `Start(ctx)`) and a concurrent runner. Make Telegram and Slack implement it.
   *No behavior change.*
2. **Multiple sources at once.** `AURORA_SOURCES` (comma list) runs N sources
   concurrently against one runtime; first failure cancels the rest. Chart takes
   `channels: [telegram, slack]`. `AURORA_CHANNEL` remains a fallback.
3. **Named manifests + bindings config.** Replace per-user manifest copies with
   `manifests:` (named) + `bindings:` (source × subjects → manifest), keeping the
   existing per-channel policy files working for back-compat.
4. **Kubernetes-informer source.** A non-interactive `Source` that watches
   resources and opens runs, with output routed to a configured sink.
5. **CRDs / operator.** Express manifests and bindings as cluster resources.

## Implemented now

Increments **1, 2, and 3**:

- `internal/source` (interface + concurrent runner with first-error
  cancellation); Telegram and Slack implement `source.Source`.
- `AURORA_SOURCES` multi-source wiring in `cmd`; the chart takes a `channels`
  list. Sources run concurrently against one runtime.
- `internal/binding`: the named-manifest `manifests:` + `bindings:` format. A
  manifest is defined once and bound to `(source, subject, scope)` tuples. The
  Telegram and Slack policy loaders auto-detect it and project it into their
  existing authorization sets; the legacy per-channel `users:` files still load.
  Manifests are digested identically, so migrating a manifest verbatim preserves
  its digest (no forced re-confirmation).

The existing channels' internals (services, state, render) are unchanged, so
behavior per channel is identical to before.

## Deferred

- The internal de-duplication of the two services into a shared driver/sink. This
  is an internal cleanup only (not user-visible) and is deliberately staged after
  the binding model lands, to avoid churning two working transports at once.
- Increment 4 (Kubernetes-informer source) — a non-interactive `Source`; needs a
  decision on output routing (which sink the run reports to).
- Increment 5 (CRDs / operator) — expressing manifests and bindings as cluster
  resources with a controller; a larger architectural commitment
  (controller-runtime, codegen, RBAC) best reviewed on its own.

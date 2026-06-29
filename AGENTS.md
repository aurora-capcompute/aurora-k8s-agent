# AGENTS.md

Aurora Kubernetes agent — a chat-controlled (Telegram and Slack), in-cluster AI
agent for Kubernetes and Helm operations. Runs are driven by a caller-supplied
Wasm brain on the event-sourced `aurora-capcompute` runtime; the brain itself is
decoupled, loaded at runtime from a Manifest's inlined brain reference (an OCI
artifact), not embedded.

## Build and test

```sh
make brain        # TinyGo Wasm brain
make test         # go test ./...
make race         # go test -race ./...
make vet          # go vet ./...
make helm-lint    # helm lint + template render
make docker       # docker build
```

## Module layout

Dependencies are resolved via the `go.work` workspace at the repository root.

```
cmd/aurora-k8s-agent/        entry point; config, source wiring, brain-pack/seal subcommands
internal/assembly/           brain provider (OCI + empty), dispatcher provider, Secret guard
internal/oci/                pull brain artifacts (wasm + declaration) from OCI registries
internal/brainspec/          brain manifest: ABI, bundled brains, and entry-point
internal/apis/               v1alpha1 control-plane types (the single Manifest CRD:
                             inlined brain + typed-ADT channels + capability tree)
internal/controller/         CRD informer + fs source; reconcile (pull brains, validate, bind)
internal/binding/            named-manifest bindings (source × subject × scope)
internal/source/             Source interface + concurrent multi-source runner
internal/channelsup/         supervises live bridges, one per channel in a Manifest
internal/chat/               transport-agnostic chat core (subscriptions, timers, policy)
internal/chat/telegram/      Telegram adapter; subpackages state (SQLite) + policy
internal/chat/slack/         Slack adapter; subpackages state (SQLite) + policy
internal/transport/telegram/ raw Telegram Bot API client
internal/transport/slack/    Slack Socket Mode + Web API client (slack-go)
internal/webapi/             HTTP + SSE API; internal/webchannel/ its channel registry
internal/secrets/, secretbox/  encrypted secret resolution for channel credentials
examples/brain/              TinyGo Wasm agent source (decoupled; not built into the binary)
charts/                      Helm chart
```

Each channel is a `source.Source` (`Kind()` + `Start(ctx)`); `cmd` builds the set
named by `AURORA_SOURCES` and runs them concurrently against one runtime via
`source.Run` (first failure cancels the rest). All sources share the runtime,
`internal/assembly`, brains, and dispatchers; they differ only in transport,
user/channel identifiers, and state. The chat layer is symmetric: an adapter under
`chat/{telegram,slack}` turns messages into runs and renders state back, over a raw
client under `transport/{telegram,slack}`. Adding a source means a transport client
plus an adapter mirroring `internal/chat/telegram` that implements `source.Source`,
plus a branch in `cmd`. See `docs/rfc-sources-and-bindings.md` for the staged plan
(named-manifest bindings, a Kubernetes-informer source, CRDs).

## Conventions

- Write simple Go. No frameworks. The one library dependency is `slack-go/slack`
  (Socket Mode envelopes/acks/reconnect are not worth hand-rolling); it is confined
  to `internal/transport/slack`, which the rest of the code never imports directly.
- Secrets never appear in Aurora manifests — use `api_key_env` references.
- The guarded dispatcher blocks Secret operations at the dispatch level.
- Mutating capabilities should require explicit per-operation approval.

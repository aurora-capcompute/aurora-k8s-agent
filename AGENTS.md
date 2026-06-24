# AGENTS.md

Aurora Kubernetes agent — Telegram-controlled, in-cluster AI agent for
Kubernetes and Helm operations.

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

Dependencies are pinned as git submodules under `third_party/`.

```
cmd/aurora-k8s-agent/   entry point; AURORA_SOURCES runs telegram and/or slack
internal/assembly/      brain embed, dispatcher provider, Secret guard
internal/source/        Source interface + concurrent multi-source runner
internal/bot/           Telegram service (service/commands/callbacks/events/render)
internal/policy/        per-user manifest and chat authorization (Telegram, int64 IDs)
internal/state/         encrypted SQLite bridge state (Telegram)
internal/telegram/      raw Telegram Bot API client
internal/slackbot/      Slack service (service/commands/events/actions/render/timer)
internal/slackpolicy/   per-user manifest and channel authorization (Slack, string IDs)
internal/slackstate/    encrypted SQLite bridge state (Slack)
internal/slack/         Slack Socket Mode + Web API client (slack-go)
brain/                  TinyGo Wasm agent source
charts/                 Helm chart
```

Each channel is a `source.Source` (`Kind()` + `Start(ctx)`); `cmd` builds the set
named by `AURORA_SOURCES` and runs them concurrently against one runtime via
`source.Run` (first failure cancels the rest). All sources share the runtime,
`internal/assembly`, brain, and dispatchers; they differ only in transport,
user/channel identifiers, and state. Adding a source means a transport client + a
service mirroring `internal/bot` that implements `source.Source`, plus a branch in
`cmd`. See `docs/rfc-sources-and-bindings.md` for the staged plan (named-manifest
bindings, a Kubernetes-informer source, CRDs).

## Conventions

- Write simple Go. No frameworks. The one library dependency is `slack-go/slack`
  (Socket Mode envelopes/acks/reconnect are not worth hand-rolling); it is confined
  to `internal/slack`, which the rest of the code never imports directly.
- Secrets never appear in Aurora manifests — use `api_key_env` references.
- The guarded dispatcher blocks Secret operations at the dispatch level.
- Mutating capabilities should require explicit per-operation approval.

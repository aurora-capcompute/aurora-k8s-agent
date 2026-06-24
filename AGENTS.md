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
cmd/aurora-k8s-agent/   entry point; AURORA_CHANNEL selects telegram|slack
internal/assembly/      brain embed, dispatcher provider, Secret guard
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

Both channels share the same runtime, `internal/assembly`, brain, and dispatchers;
they differ only in transport, user/channel identifiers, and state. Adding a
channel means a transport client + a service mirroring `internal/bot`, plus a
branch in `cmd`.

## Conventions

- Write simple Go. No frameworks. The one library dependency is `slack-go/slack`
  (Socket Mode envelopes/acks/reconnect are not worth hand-rolling); it is confined
  to `internal/slack`, which the rest of the code never imports directly.
- Secrets never appear in Aurora manifests — use `api_key_env` references.
- The guarded dispatcher blocks Secret operations at the dispatch level.
- Mutating capabilities should require explicit per-operation approval.

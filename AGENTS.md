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
cmd/aurora-k8s-agent/   entry point, config, health server
internal/assembly/      brain embed, dispatcher provider, Secret guard
internal/bot/           Telegram service split across:
  service.go              core loop, subscribe, recover
  commands.go             /help, /new, /status, /history, /cancel, /retry
  callbacks.go            inline keyboard callbacks, task approval
  events.go               runtime event handling (run updates, task cards)
  render.go               formatting, chunking, redaction helpers
internal/policy/        per-user manifest and chat authorization
internal/state/         encrypted SQLite bridge state
internal/telegram/      raw Telegram Bot API client
brain/                  TinyGo Wasm agent source
charts/                 Helm chart
```

## Conventions

- Write simple Go. No frameworks.
- Secrets never appear in Aurora manifests — use `api_key_env` references.
- The guarded dispatcher blocks Secret operations at the dispatch level.
- Mutating capabilities should require explicit per-operation approval.

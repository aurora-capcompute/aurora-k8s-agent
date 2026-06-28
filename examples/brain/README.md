# Example brain: `kubernetes-agent`

The agent ships **no** brain. A brain is a separate wasm artifact you build and
hand to the agent at runtime — either by reference from a `Brain` CRD (the
control plane hot-loads it) or via `AURORA_BRAINS`. This directory is the
reference brain the project used to embed: a TinyGo Kubernetes/Helm operator.

## Files

- `agent.go` — the brain (TinyGo, `//go:build tinygo`, Extism PDK). It runs an
  agentic loop, calling `openai.chat` to plan and the host-granted `k8s.*` /
  `helm.*` tools to act. `agent.input` / `agent.finish` are ABI host calls.
- `build.sh` — compiles `agent.go` to `dist/kubernetes-agent.wasm` (TinyGo).

## Build and pack into a registry-less OCI layout

```sh
# 1. Compile the brain to wasm (needs TinyGo 0.41.x).
sh examples/brain/build.sh

# 2. Pack wasm into an on-disk OCI image layout.
go build -o bin/aurora-k8s-agent ./cmd/aurora-k8s-agent
./bin/aurora-k8s-agent pack-brain \
  --brain kubernetes-agent:examples/brain/dist/kubernetes-agent.wasm \
  --out examples/brain/dist/layout
```

The layout is now loadable with **no registry** as
`oci-layout:<abs-path>/examples/brain/dist/layout:latest` — from a `Brain` CRD's
`artifact`, or `AURORA_BRAINS`. To publish it to a registry instead, push the
layout with `oras cp --from-oci-layout dist/layout:latest ghcr.io/you/brain:tag`
and reference the registry tag.

See [`../telegram-local`](../telegram-local) to run it locally over Telegram and
[`../telegram-k8s`](../telegram-k8s) to deploy it.

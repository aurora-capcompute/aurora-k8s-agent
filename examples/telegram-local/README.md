# Run the brain over Telegram, locally

Replicates the old embedded "brain + Telegram" setup with **no Kubernetes and no
registry**. The agent boots brain-less and loads the example brain from an on-disk
OCI layout, driven by the same single `Manifest` resource you would apply in a
cluster (here fed through the filesystem control plane, `AURORA_CONTROL_PLANE=fs`).

## Steps

```sh
cp examples/telegram-local/.env.example examples/telegram-local/.env
# edit .env: TELEGRAM_BOT_TOKEN, TELEGRAM_USER_ID, TELEGRAM_CHAT_ID, OPENAI_API_KEY
sh examples/telegram-local/run.sh
```

`run.sh` builds the agent and brain, packs the brain into
`examples/brain/dist/layout`, seals the bot token, renders
[`resources/manifest.yaml`](resources/manifest.yaml) into `dist/resources` (with
the layout path, sealed token, and ids filled in), then starts the agent. Message
your bot from the configured user and it plans with the LLM and acts via the
granted `k8s.*` / `helm.*` tools. Requires Go and TinyGo 0.41.x on the host.

## What's happening

- The agent starts with **zero brains** — removing the embed "doesn't break
  anything": the process is healthy, only brain runs need a brain.
- The fs control plane resolves the Manifest, pulls the brain from
  `oci-layout:…` (no registry), and hot-registers it via `runtime.SetBrains`.
- The channel supervisor decrypts the Telegram token (`AURORA_SECRET_KEY`) and
  opens the bridge; the Manifest's capability grant scopes what the brain may do.

Generated keys live in `dist/secrets.env` so state persists across runs. Delete
`dist/` to reset. The exact same `manifest.yaml` deploys to a cluster — see
[`../telegram-k8s`](../telegram-k8s).

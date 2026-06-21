# aurora-k8s-agent

Deployable Aurora assembly for operating Kubernetes and Helm through Telegram.

The agent combines:

- `aurora-capcompute` durable threads, runs, journals, tasks, and replay;
- native Kubernetes and Helm dispatchers;
- an OpenAI-compatible cognition dispatcher;
- a TinyGo/Wasm Kubernetes-agent brain;
- per-Telegram-user manifests and named elevation profiles;
- SQLite persistence on a Kubernetes PVC;
- inline Telegram approval and denial controls.

This is an in-cluster agent, not a CRD reconciliation operator.

## Security model

Three independent boundaries apply:

1. Kubernetes RBAC is the hard ceiling. The default chart creates namespace
   Roles only in `rbac.targetNamespaces`.
2. The ConfigMap maps Telegram user IDs to allowed chat IDs, baseline Aurora
   manifests, and administrator-defined elevation profiles.
3. Mutating Kubernetes and Helm capability calls yield durable approval tasks.
   Only the Telegram user who started the session can resolve those tasks.

An elevation profile is armed through `/privileges`, consumed by the next run,
and bound to that run's capcompute session. It is revoked when the run reaches a
terminal state. Users cannot submit arbitrary capability overrides.

Policy changes trigger a pod rollout through a ConfigMap checksum. The bridge
also stores each user's policy digest and rotates the conversation before the
next request if the digest changed, preventing old threads from retaining
revoked permissions.

## Restart recovery

The PVC contains:

- Aurora threads, runs, history, replay journals, tasks, and leases;
- Telegram update inbox and polling offset;
- `(user, chat) → thread` mappings;
- armed and consumed elevation profiles;
- run/status and approval message IDs;
- encrypted task tokens and processed callback IDs.

Extism plugin instances are process-local and are not serialized. After a
restart, the application recreates capcompute sessions from the persisted
effective manifest and replays the journal. Waiting tasks remain durable; an
approved task resumes through the same replay path. Interrupted active runs are
retried with `RetryResume`.

Telegram updates are inserted into the durable inbox before the polling offset
advances, making prompt and callback processing idempotent across crashes.

## Telegram UX

Private chats are accepted directly. In groups, the bot responds only to a
command, direct mention, reply to the bot, or approval callback. Conversations
are isolated by both Telegram user ID and chat ID.

Commands:

- `/status` — active session state;
- `/history` — recent conversation;
- `/privileges` — select and confirm a one-session elevation profile;
- `/cancel` — stop the active session and revoke elevation;
- `/retry` — reconstruct and resume the latest interrupted session;
- `/new` — rotate to a fresh conversation;
- `/help` — command and safety summary.

The bot edits one run status message as execution progresses and sends separate
approval cards containing the operation, arguments, summary, and expiry.

## Install

Create a Kubernetes Secret with the four required keys:

```sh
kubectl -n aurora create secret generic aurora-secrets \
  --from-literal=telegram-bot-token='<BotFather token>' \
  --from-literal=task-secret="$(openssl rand -hex 32)" \
  --from-literal=state-key="$(openssl rand -base64 32)" \
  --from-literal=openai-api-key='<provider API key>'
```

Or as YAML:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aurora-secrets
  namespace: aurora
type: Opaque
stringData:
  telegram-bot-token: "<BotFather token>"
  task-secret: "<long random string — HMAC secret for durable task tokens>"
  state-key: "<32 random bytes as base64, or a long passphrase — encrypts Telegram state>"
  openai-api-key: "<OpenAI or compatible provider API key>"
```

| Key | Purpose |
|-----|---------|
| `telegram-bot-token` | Telegram Bot API token from @BotFather |
| `task-secret` | HMAC secret for durable approval task webhook tokens. Must stay stable across restarts. |
| `state-key` | AES-256 key for encrypting stored Telegram tokens. Base64-encoded 32 bytes, or a passphrase (SHA-256 hashed). |
| `openai-api-key` | API key for the OpenAI-compatible LLM provider. The key name can be changed via `llmSecretKey` in values. |

Configure users and namespaces in a values file:

```yaml
secretName: aurora-secrets

rbac:
  targetNamespaces: [default, observability]

policy:
  users:
    "123456789":
      allowed_chats: [123456789]
      manifest:
        version: 2
        brain: kubernetes-agent
        system_prompt: Inspect before changing resources.
        capabilities:
          - name: openai.chat
            settings:
              base_url: https://api.openai.com/v1
              api_key_env: OPENAI_API_KEY
              default_model: gpt-5.5
              allowed_models: [gpt-5.5]
              require_approval: false
          - name: k8s.get
            settings: {namespaces: [default, observability]}
          - name: k8s.list
            settings: {namespaces: [default, observability]}
          - name: k8s.logs
            settings: {namespaces: [default, observability]}
          - name: helm.list
            settings: {namespaces: [default, observability]}
      elevation_profiles:
        observability-write:
          label: Observability write access
          description: Modify resources and Helm releases in observability.
          ttl: 10m
          overrides:
            - name: k8s.apply
              settings: {namespaces: [observability], require_approval: true}
            - name: k8s.delete
              settings: {namespaces: [observability], require_approval: true}
            - name: helm.upgrade
              settings:
                namespaces: [observability]
                charts: ["prometheus-community/*"]
                require_approval: true
```

Install:

```sh
helm upgrade --install aurora ./charts/aurora-k8s-agent \
  --namespace aurora --create-namespace \
  -f values.production.yaml
```

An empty Helm chart allowlist permits all chart references in the allowed
namespaces. Use an explicit list in production.

## OpenAI-compatible providers

`openai.chat` supports OpenAI and compatible gateways. Configure `base_url`,
`default_model`, `allowed_models`, `api_key_env`, and optional
`headers_from_env` in each user's manifest. The API key itself remains in a
Kubernetes Secret and is never persisted in an Aurora manifest.

The cognition capability is dispatchable by the embedded brain but filtered
from the operational tools shown to the model. Only the session's effective
Kubernetes and Helm capabilities appear in its tool list.

## Development

Clone pinned dependencies:

```sh
git submodule update --init --recursive
```

Build and verify:

```sh
make brain
make race
make vet
make helm-lint
make docker
```

The repository pins its Aurora dependencies as Git submodules because the
upstream modules currently use short module paths and local Go replacements.

## Operational constraints

- Exactly one replica is supported because SQLite uses a `ReadWriteOnce` PVC.
- The Deployment uses `Recreate` strategy.
- Cluster-wide RBAC is an explicit opt-in.
- Cluster-scoped resources and CRDs are unavailable with namespace Roles.
- `/livez` reports process health; `/readyz` becomes ready only after policy,
  storage, runtime recovery, and Telegram identity validation succeed.

# aurora-k8s-agent

Deployable Aurora assembly for operating Kubernetes and Helm through Telegram.

The agent combines:

- `aurora-capcompute` durable threads, runs, journals, tasks, and replay;
- native Kubernetes and Helm dispatchers;
- an OpenAI-compatible cognition dispatcher;
- a TinyGo/Wasm Kubernetes-agent brain;
- per-Telegram-user manifests and chat authorization;
- SQLite persistence on a Kubernetes PVC;
- inline Telegram approval and denial controls.

This is an in-cluster agent, not a CRD reconciliation operator.

## Security model

Three independent boundaries apply:

1. Kubernetes RBAC is the hard ceiling. The default chart creates namespace
   Roles only in `rbac.targetNamespaces`.
2. The ConfigMap maps Telegram user IDs to allowed chat IDs, baseline Aurora
   manifests, and their configured capabilities.
3. Capabilities configured with `require_approval: true` yield durable approval
   tasks. Only the Telegram user who started the session can resolve those
   tasks.

There is no session-scoped privilege escalation layer. Administrators grant
capabilities directly in each user's manifest; Kubernetes RBAC still limits
what those capabilities can do.

Policy changes trigger a pod rollout through a ConfigMap checksum. The bridge
also stores each user's policy digest and rotates the conversation before the
next request if the digest changed, preventing old threads from retaining
revoked permissions.

## Restart recovery

The PVC contains:

- Aurora threads, runs, history, replay journals, tasks, and leases;
- Telegram update inbox and polling offset;
- `(user, chat) → thread` mappings;
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
- `/cancel` — stop the active session;
- `/retry` — reconstruct and resume the latest interrupted session;
- `/new` — rotate to a fresh conversation;
- `/help` — command and safety summary.

The bot edits one run status message as execution progresses and sends separate
approval cards containing the operation, arguments, summary, and expiry.

## Sources (channels)

A **source** is a first-class caller of the agent: it owns a transport,
identifies a subject, and drives runs against the shared runtime. Telegram and
Slack are the interactive sources today, and they can run **at the same time**
against one runtime — set `AURORA_SOURCES` to a comma-separated list (chart:
`channels: [telegram, slack]`). The legacy single-channel `AURORA_CHANNEL` /
`channel:` still works as a fallback. Each source keeps its own bridge state
(`telegram.db`, `slack.db`) and reads its users from the same policy file.

## Slack source

The agent can run on Slack alongside or instead of Telegram — add `slack` to
`AURORA_SOURCES` (chart: `channels: [slack]`). It uses Slack **Socket Mode**, so
it still needs no public ingress. DM the bot or @mention it; mutating
capabilities surface **Approve / Deny** buttons resolvable only by the user who
started the run; the slash command is `/aurora help|new|status|cancel`.

Slack app setup: enable Socket Mode and generate an app-level token
(`connections:write`); bot scopes `app_mentions:read`, `chat:write`, `commands`,
`im:history`, `im:read`, `im:write`; subscribe to `app_mention` and `message.im`;
add a `/aurora` slash command. The Secret then needs `slack-app-token` (`xapp-…`)
and `slack-bot-token` (`xoxb-…`) instead of `telegram-bot-token`.

Policy for Slack is keyed by Slack user IDs (`U…`) with `allowed_channels`
(channel/DM IDs `C…`/`G…`/`D…`) rather than numeric IDs and `allowed_chats`.
Conversations are isolated by Slack user ID and channel.

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

### Named manifests and bindings

Instead of copying a manifest into every user, you can define each manifest once
and **bind** it to a set of `(source, subject, scope)` tuples. The agent
auto-detects this format (chart: set `policy.manifests` and `policy.bindings`
instead of `policy.users`):

```yaml
policy:
  version: 2
  manifests:
    ops:
      version: 2
      brain: kubernetes-agent
      capabilities:
        - name: openai.chat
          settings: {api_key_env: OPENAI_API_KEY, default_model: gpt-5.5}
        - name: k8s.get
          settings: {namespaces: [default]}
  bindings:
    # Same manifest, two sources, different subjects/scopes.
    - source: telegram
      manifest: ops
      users: ["123456789"]      # numeric Telegram user IDs
      scopes: ["123456789"]     # numeric chat IDs
    - source: slack
      manifest: ops
      users: ["U0123"]          # Slack user IDs
      scopes: ["C0001", "D0002"]  # Slack channel/DM IDs
```

The legacy per-channel `policy.users` form still works unchanged. A manifest
migrated verbatim keeps the same digest, so existing sessions are not forced to
re-confirm.

Install:

```sh
helm upgrade --install aurora ./charts/aurora-k8s-agent \
  --namespace aurora --create-namespace \
  -f values.production.yaml
```

An empty Helm chart allowlist permits all chart references in the allowed
namespaces. Use an explicit list in production. The default values remain
read-only; add mutating capabilities explicitly where needed.

## Images and deployment

CI publishes container images to the GitHub Container Registry under
`ghcr.io/aurora-capcompute/aurora-k8s-agent`:

| Trigger | Tags | Use |
|---------|------|-----|
| push to `main` (`ci.yml`) | `main`, `latest`, `sha-<shortsha>` | track main / pin an exact commit |
| `vX.Y.Z` git tag (`release.yml`) | `X.Y.Z`, `X.Y` (multi-arch, signed, SBOM) | versioned releases |

The `sha-<shortsha>` tags are immutable — pin to one for reproducible deploys and
roll back by changing a single value. `main`/`latest` move with the branch.

The chart already defaults `image.repository` to that path, so a deploy only needs
a tag:

```sh
helm upgrade --install aurora oci://ghcr.io/aurora-capcompute/charts/aurora-k8s-agent \
  --namespace aurora --create-namespace \
  -f values.production.yaml \
  --set image.tag=sha-1a2b3c4
```

(The chart is also published to GHCR on release; or use a local
`./charts/aurora-k8s-agent` checkout.)

### Pulling a private image

GHCR packages are private by default, so the cluster needs an image pull secret.
Create one from a token with `read:packages`, then reference it in values:

```sh
kubectl -n aurora create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io \
  --docker-username='<github-username>' \
  --docker-password='<PAT or GITHUB_TOKEN with read:packages>'
```

```yaml
# values.production.yaml
image:
  tag: sha-1a2b3c4
imagePullSecrets:
  - name: ghcr-pull
```

### Self-upgrade via the agent

Because the agent operates Helm, it can roll itself to a newly published image —
just tell it (over Telegram) to upgrade its own release to the desired tag. This
requires, in that user's manifest, a `helm.upgrade` capability scoped to the
agent's own namespace and chart (keep `require_approval: true`), and the
`ghcr-pull` secret already present in the cluster:

```yaml
- name: helm.upgrade
  settings:
    namespaces: [aurora]
    charts: ["oci://ghcr.io/aurora-capcompute/charts/aurora-k8s-agent"]
    require_approval: true
```

The agent then runs the same `helm upgrade … --set image.tag=<new-tag>` shown
above. Granting an agent the ability to replace its own image is powerful; scope
the capability tightly and leave approval on.

## Timers

The `timer.set` capability lets the agent pause a run for a relative duration and
be replayed when it fires. The agent calls it with `duration_seconds` (and an
optional `label`); the run yields a durable timer task and its status message shows
when it will continue. A scheduler resolves the task once the duration elapses,
which resumes the run from exactly where it paused, so the agent can follow up
(for example, send a reminder). Timers are persisted with the run and re-armed on
restart; any whose fire time already passed fire immediately on recovery.

Grant it per user with an optional `max_duration_ms` bound (default 24h):

```yaml
- name: timer.set
  settings: {max_duration_ms: 86400000}
```

Timers rely on durable tasks not having a shorter expiry than the timer; the agent
does not configure a task TTL, so timers up to `max_duration_ms` are safe.

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

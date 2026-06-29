# Deploy the brain over Telegram on Kubernetes

The agent image ships no brain. Here the example brain is baked into a derived
image as an OCI layout and referenced inline by a `Manifest` CRD — **no registry
required** — while the controller hot-loads it into the running agent.

## 1. Build a derived image with the brain baked in

```sh
sh examples/brain/build.sh
go build -o bin/aurora-k8s-agent ./cmd/aurora-k8s-agent
./bin/aurora-k8s-agent pack-brain \
  --brain kubernetes-agent:examples/brain/dist/kubernetes-agent.wasm \
  --out examples/brain/dist/layout

docker build -f examples/telegram-k8s/Dockerfile \
  --build-arg BASE=ghcr.io/aurora-capcompute/aurora-k8s-agent:latest \
  -t ghcr.io/you/aurora-k8s-agent-k8sbrain:latest .
docker push ghcr.io/you/aurora-k8s-agent-k8sbrain:latest
```

(Alternatively, skip the bake: `oras cp --from-oci-layout
examples/brain/dist/layout:latest ghcr.io/you/brain-k8s:1.0`, use the stock agent
image, and set `Manifest.spec.brain.artifact: ghcr.io/you/brain-k8s:1.0`.)

## 2. Create the Secret and install the chart with the controller enabled

```sh
kubectl create namespace aurora
# edit secret.example.yaml first (task-secret, state-key, secret-key, openai key)
kubectl apply -f examples/telegram-k8s/secret.example.yaml

helm install aurora charts/aurora-k8s-agent -n aurora \
  --set image.repository=ghcr.io/you/aurora-k8s-agent-k8sbrain \
  --set image.tag=latest \
  --set secretName=aurora-secrets
```

## 3. Seal secrets and apply the manifest

```sh
KEY=<secret-key from the Secret>

# Seal the Telegram bot token → paste into manifest.yaml spec.channels[0].telegram.botToken.ciphertext
printf %s "$TELEGRAM_BOT_TOKEN" | AURORA_SECRET_KEY=$KEY aurora-k8s-agent seal-secret

# Seal the OpenAI API key → paste into manifest.yaml capabilities openai.chat api_key.ciphertext
printf %s "$OPENAI_API_KEY" | AURORA_SECRET_KEY=$KEY aurora-k8s-agent seal-secret

# Set user/chat IDs in manifest.yaml, then apply:
kubectl apply -n aurora -f examples/telegram-k8s/manifest.yaml
```

The controller pulls the brain from `oci-layout:/brains/kubernetes-agent:latest`,
registers it via `runtime.SetBrains`, and the supervisor opens the Telegram
bridge. Both the Telegram bot token and the OpenAI API key are encrypted at rest
in the Manifest and resolved at bridge startup — neither appears in environment
variables or Kubernetes Secrets.

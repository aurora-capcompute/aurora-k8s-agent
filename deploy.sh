#!/bin/sh
set -eu

# Aurora K8s Agent deployment script
#
# Usage:
#   TELEGRAM_BOT_TOKEN=... LLM_API_KEY=... VALUES_FILE=my-values.yaml ./deploy.sh
#
# Required environment variables:
#   TELEGRAM_BOT_TOKEN  — Telegram Bot API token from @BotFather
#   LLM_API_KEY         — API key for the LLM provider (DeepSeek, OpenAI, etc.)
#   VALUES_FILE         — Path to Helm values file (no default — must be provided)
#
# Optional environment variables:
#   NAMESPACE           — Kubernetes namespace (default: aurora)
#   RELEASE             — Helm release name (default: aurora)
#   IMAGE_TAG           — Docker image tag (default: v0.1.0)
#   KUBECTL             — kubectl command (default: kubectl, or k3s kubectl on k3s)
#   HELM                — helm command (default: helm)
#   RUNTIME             — Container runtime: docker, k3s, kind (default: auto-detect)

NAMESPACE="${NAMESPACE:-aurora}"
RELEASE="${RELEASE:-aurora}"
IMAGE_TAG="${IMAGE_TAG:-v0.1.0}"
IMAGE="aurora-k8s-agent:${IMAGE_TAG}"

die() { echo "error: $1" >&2; exit 1; }

[ -n "${TELEGRAM_BOT_TOKEN:-}" ] || die "TELEGRAM_BOT_TOKEN is required"
[ -n "${LLM_API_KEY:-}" ]        || die "LLM_API_KEY is required"
[ -n "${VALUES_FILE:-}" ]        || die "VALUES_FILE is required (path to your Helm values file)"
[ -f "$VALUES_FILE" ]            || die "$VALUES_FILE not found"
[ -f Dockerfile ]                || die "Dockerfile not found — run from the aurora-k8s-agent directory"

# Auto-detect container runtime
if [ -z "${RUNTIME:-}" ]; then
  if command -v k3s >/dev/null 2>&1; then
    RUNTIME=k3s
  elif command -v kind >/dev/null 2>&1; then
    RUNTIME=kind
  else
    RUNTIME=docker
  fi
fi

KUBECTL="${KUBECTL:-kubectl}"
HELM="${HELM:-helm}"
if [ "$RUNTIME" = "k3s" ] && [ "$KUBECTL" = "kubectl" ]; then
  KUBECTL="k3s kubectl"
fi

echo "=== 1. Initialize submodules ==="
if [ -f .gitmodules ]; then
  git submodule update --init --recursive
fi

echo "=== 2. Create namespace ==="
$KUBECTL create namespace "$NAMESPACE" --dry-run=client -o yaml | $KUBECTL apply -f -

echo "=== 3. Create secrets ==="
$KUBECTL -n "$NAMESPACE" create secret generic aurora-secrets \
  --from-literal=telegram-bot-token="$TELEGRAM_BOT_TOKEN" \
  --from-literal=task-secret="$(openssl rand -hex 32)" \
  --from-literal=state-key="$(openssl rand -base64 32)" \
  --from-literal=openai-api-key="$LLM_API_KEY" \
  --dry-run=client -o yaml | $KUBECTL apply -f -

echo "=== 4. Build Docker image ==="
docker build -t "$IMAGE" .

echo "=== 5. Load image into cluster ==="
case "$RUNTIME" in
  k3s)
    docker save "$IMAGE" | k3s ctr images import -
    ;;
  kind)
    kind load docker-image "$IMAGE"
    ;;
  *)
    echo "  (assuming image is available to the cluster)"
    ;;
esac

echo "=== 6. Install with Helm ==="
DEPLOY_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
$HELM upgrade --install "$RELEASE" ./charts/aurora-k8s-agent \
  --namespace "$NAMESPACE" \
  -f "$VALUES_FILE" \
  --set image.repository="${IMAGE%:*}" \
  --set image.tag="${IMAGE_TAG}" \
  --set image.pullPolicy=Never \
  --set "podAnnotations.aurora\\.dev/deployed-at=${DEPLOY_TS}"

echo "=== 7. Wait for rollout ==="
$KUBECTL -n "$NAMESPACE" rollout status deployment/"$RELEASE" --timeout=120s

echo "=== 8. Check logs ==="
$KUBECTL -n "$NAMESPACE" logs -l app.kubernetes.io/instance="$RELEASE" --tail=20

echo ""
echo "=== Done! ==="
echo "Useful commands:"
echo "  $KUBECTL -n $NAMESPACE logs -f -l app.kubernetes.io/instance=$RELEASE"
echo "  $KUBECTL -n $NAMESPACE get pods"
echo "  $KUBECTL -n $NAMESPACE describe deployment $RELEASE"

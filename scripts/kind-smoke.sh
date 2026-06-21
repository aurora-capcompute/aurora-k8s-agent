#!/bin/sh
set -eu

cluster="${KIND_CLUSTER_NAME:-aurora-agent}"
image="${AURORA_IMAGE:-aurora-k8s-agent:smoke}"

kind create cluster --name "$cluster"
trap 'kind delete cluster --name "$cluster"' EXIT

docker build -t "$image" .
kind load docker-image --name "$cluster" "$image"

kubectl create namespace aurora
kubectl -n aurora create deployment telegram-mock --image=python:3.13-alpine -- \
  python -c 'from http.server import BaseHTTPRequestHandler,HTTPServer
import json
class H(BaseHTTPRequestHandler):
 def do_POST(self):
  result={"id":99,"username":"aurora_smoke_bot"} if self.path.endswith("/getMe") else []
  body=json.dumps({"ok":True,"result":result}).encode()
  self.send_response(200); self.send_header("Content-Type","application/json")
  self.send_header("Content-Length",str(len(body))); self.end_headers(); self.wfile.write(body)
 def log_message(self,*args): pass
HTTPServer(("0.0.0.0",8080),H).serve_forever()'
kubectl -n aurora expose deployment telegram-mock --port 8080
kubectl -n aurora rollout status deployment/telegram-mock --timeout=120s

helm upgrade --install aurora charts/aurora-k8s-agent \
  --namespace aurora \
  --set image.repository="${image%:*}" \
  --set image.tag="${image##*:}" \
  --set image.pullPolicy=Never \
  --set secrets.telegramBotToken=smoke \
  --set secrets.taskSecret=smoke-task-secret \
  --set secrets.stateKey=smoke-state-key \
  --set secrets.llmAPIKey=smoke \
  --set 'extraEnv[0].name=TELEGRAM_API_BASE_URL' \
  --set 'extraEnv[0].value=http://telegram-mock.aurora.svc:8080'

kubectl -n aurora rollout status deployment/aurora-aurora-k8s-agent --timeout=120s
kubectl -n aurora get --raw \
  "/api/v1/namespaces/aurora/pods/$(kubectl -n aurora get pod -l app.kubernetes.io/instance=aurora -o jsonpath='{.items[0].metadata.name}'):8080/proxy/readyz" |
  grep -q ready

kubectl auth can-i list pods \
  --namespace default \
  --as system:serviceaccount:aurora:aurora-aurora-k8s-agent
kubectl auth can-i create deployments.apps \
  --namespace default \
  --as system:serviceaccount:aurora:aurora-aurora-k8s-agent
if kubectl auth can-i create deployments.apps \
  --namespace kube-system \
  --as system:serviceaccount:aurora:aurora-aurora-k8s-agent | grep -q '^yes$'; then
  echo "namespace RBAC isolation failed" >&2
  exit 1
fi

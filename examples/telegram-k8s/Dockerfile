# Bakes the example brain's OCI layout into the agent image so a Brain CRD can
# load it with no registry (artifact: oci-layout:/brains/kubernetes-agent:latest).
#
# Build from the repo root AFTER packing the brain layout:
#   sh examples/brain/build.sh
#   ./bin/aurora-k8s-agent pack-brain \
#     --wasm examples/brain/dist/kubernetes-agent.wasm \
#     --manifest examples/brain/manifest.json \
#     --out examples/brain/dist/layout
#   docker build -f examples/telegram-k8s/Dockerfile \
#     -t ghcr.io/you/aurora-k8s-agent-k8sbrain:latest .
ARG BASE=ghcr.io/aurora-capcompute/aurora-k8s-agent:latest
FROM ${BASE}
COPY examples/brain/dist/layout /brains/kubernetes-agent

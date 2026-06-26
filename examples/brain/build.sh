#!/bin/sh
# Build the example Kubernetes-operator brain to wasm. The agent no longer embeds
# a brain; pack the output into an OCI layout with `aurora-k8s-agent pack-brain`
# and load it via a Brain CRD's `artifact` (or AURORA_BRAINS).
set -eu

cd "$(dirname "$0")/../.." # repo (module) root
: "${GOCACHE:=/tmp/aurora-k8s-agent-go-build}"
: "${XDG_CACHE_HOME:=/tmp/aurora-k8s-agent-tinygo-cache}"
export GOCACHE XDG_CACHE_HOME

mkdir -p examples/brain/dist
tinygo build \
  -target wasip1 \
  -buildmode=c-shared \
  -tags tinygo \
  -o examples/brain/dist/kubernetes-agent.wasm \
  ./examples/brain

echo "built examples/brain/dist/kubernetes-agent.wasm"

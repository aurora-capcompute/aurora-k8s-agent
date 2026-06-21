#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
: "${GOCACHE:=/tmp/aurora-k8s-agent-go-build}"
: "${XDG_CACHE_HOME:=/tmp/aurora-k8s-agent-tinygo-cache}"
export GOCACHE XDG_CACHE_HOME

tinygo build \
  -target wasip1 \
  -buildmode=c-shared \
  -tags tinygo \
  -o internal/assembly/kubernetes-agent.wasm \
  ./brain

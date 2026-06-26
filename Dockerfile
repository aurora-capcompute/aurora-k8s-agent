# syntax=docker/dockerfile:1.7

# The agent ships no brain: it boots brain-less and loads brains at runtime from
# Brain CRDs (or AURORA_BRAINS). The example brain lives under examples/ and is
# built/packed separately (see examples/telegram-k8s).
FROM golang:1.26-alpine AS build
RUN apk add --no-cache build-base git
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -linkmode external -extldflags '-static'" \
      -o /out/aurora-k8s-agent ./cmd/aurora-k8s-agent

FROM alpine/helm:4.1.3 AS helm

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 65532 aurora && adduser -D -u 65532 -G aurora aurora
COPY --from=helm /usr/bin/helm /usr/local/bin/helm
COPY --from=build /out/aurora-k8s-agent /usr/local/bin/aurora-k8s-agent
USER 65532:65532
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/aurora-k8s-agent"]

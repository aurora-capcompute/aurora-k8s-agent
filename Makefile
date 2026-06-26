.PHONY: brain test race vet build helm-lint docker

# The agent no longer embeds a brain; build targets don't depend on one. This
# builds the example brain (TinyGo) and packs it into an OCI layout you can load
# via AURORA_BRAINS=oci-layout:examples/brain/dist/layout or a Brain CRD.
brain:
	sh examples/brain/build.sh

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=1 go build -o bin/aurora-k8s-agent ./cmd/aurora-k8s-agent

helm-lint:
	helm lint charts/aurora-k8s-agent
	helm template aurora charts/aurora-k8s-agent --set image.repository=example/aurora-k8s-agent --set image.tag=test >/dev/null

docker:
	docker build -t aurora-k8s-agent:dev .

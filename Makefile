.PHONY: brain test race vet build helm-lint docker

brain:
	sh brain/build.sh

test: brain
	go test ./...

race: brain
	go test -race ./...

vet: brain
	go vet ./...

build: brain
	CGO_ENABLED=1 go build -o bin/aurora-k8s-agent ./cmd/aurora-k8s-agent

helm-lint:
	helm lint charts/aurora-k8s-agent
	helm template aurora charts/aurora-k8s-agent --set image.repository=example/aurora-k8s-agent --set image.tag=test >/dev/null

docker:
	docker build -t aurora-k8s-agent:dev .

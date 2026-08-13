.PHONY: test build image image-base validate release-archives

VERSION ?= $(shell cat VERSION)
TAG := v$(VERSION)

test:
	go test ./cmd/... ./internal/...
	cd web && npm ci && npm run lint && npm run build

build:
	mkdir -p bin
	go build -o bin/agenthub ./cmd/agenthub
	go build -o bin/agenthub-operator ./cmd/operator

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) --build-arg BUILD_TIME=$$(date -u +%Y-%m-%dT%H:%M:%SZ) -t agenthub:$(TAG) .

image-base:
	docker build -f Dockerfile.base -t agenthub-base:$(TAG) .

validate:
	kubectl kustomize deploy/kubernetes >/dev/null
	AGENTHUB_BOOTSTRAP_ADMIN=validation \
	AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=validation-password-only \
	AGENTHUB_ENCRYPTION_KEY=01234567890123456789012345678901 \
	docker compose config --quiet

release-archives: image image-base
	mkdir -p release
	docker save agenthub:$(TAG) | gzip -9 > release/agenthub-$(TAG).tar.gz
	docker save agenthub-base:$(TAG) | gzip -9 > release/agenthub-base-$(TAG).tar.gz

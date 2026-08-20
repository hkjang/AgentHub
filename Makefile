.PHONY: test build image image-base image-langflow validate release-archives

VERSION ?= $(shell cat VERSION)
TAG := v$(VERSION)
# The runtime base image is versioned separately: it is several GB and is only
# rebuilt when Dockerfile.base or what it copies in changes, so most control
# plane releases keep pointing at an older base tag.
BASE_VERSION ?= $(shell cat BASE_VERSION)
BASE_TAG := v$(BASE_VERSION)
LANGFLOW_VERSION ?= $(shell cat LANGFLOW_VERSION)
LANGFLOW_TAG := v$(LANGFLOW_VERSION)

test:
	go test -race ./cmd/... ./internal/...
	cd web && npm ci && npm run lint && npm run build

build:
	mkdir -p bin
	go build -o bin/agenthub ./cmd/agenthub
	go build -o bin/agenthub-operator ./cmd/operator

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg BASE_VERSION=$(BASE_VERSION) --build-arg LANGFLOW_VERSION=$(LANGFLOW_VERSION) --build-arg COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) --build-arg BUILD_TIME=$$(date -u +%Y-%m-%dT%H:%M:%SZ) -t agenthub:$(TAG) .

image-base:
	docker build -f Dockerfile.base -t agenthub-base:$(BASE_TAG) .

image-langflow:
	docker build -f Dockerfile.langflow -t agenthub-langflow:$(LANGFLOW_TAG) .

validate:
	kubectl kustomize deploy/kubernetes >/dev/null
	AGENTHUB_BOOTSTRAP_ADMIN=validation \
	AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=validation-password-only \
	AGENTHUB_ENCRYPTION_KEY=01234567890123456789012345678901 \
	docker compose config --quiet

# A GitHub release asset may not exceed 2 GiB. Archives are streamed through
# split so an oversized image lands as .part-aa/.part-ab/... ; anything that fits
# collapses back to a plain .tar.gz. Reassemble with `cat <name>.part-* > <name>`.
RELEASE_CHUNK ?= 1900M

release-archives: image image-base image-langflow
	mkdir -p release
	$(call package_image,agenthub:$(TAG),agenthub-$(TAG).tar.gz)
	$(call package_image,agenthub-base:$(BASE_TAG),agenthub-base-$(BASE_TAG).tar.gz)
	$(call package_image,agenthub-langflow:$(LANGFLOW_TAG),agenthub-langflow-$(LANGFLOW_TAG).tar.gz)
	cd release && sha256sum -- agenthub-* > SHA256SUMS
	ls -lh release

define package_image
	docker save $(1) | gzip -9 | split -b $(RELEASE_CHUNK) - release/$(2).part-
	@parts=$$(ls release/$(2).part-* | wc -l); \
	if [ "$$parts" -eq 1 ]; then mv release/$(2).part-aa release/$(2); echo "packaged $(1) -> release/$(2)"; \
	else echo "packaged $(1) -> $$parts parts of release/$(2)"; fi
endef

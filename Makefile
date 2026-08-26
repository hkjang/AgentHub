.PHONY: test build image image-base image-langflow image-qwencode image-jupyter image-goose image-holmes image-browsercode image-opencodereview image-orca image-openhands image-pi image-nodered image-n8n validate release-archives

VERSION ?= $(shell cat VERSION)
TAG := v$(VERSION)
# The runtime base image is versioned separately: it is several GB and is only
# rebuilt when Dockerfile.base or what it copies in changes, so most control
# plane releases keep pointing at an older base tag.
BASE_VERSION ?= $(shell cat BASE_VERSION)
BASE_TAG := v$(BASE_VERSION)
LANGFLOW_VERSION ?= $(shell cat LANGFLOW_VERSION)
LANGFLOW_TAG := v$(LANGFLOW_VERSION)
QWENCODE_VERSION ?= $(shell cat QWENCODE_VERSION)
QWENCODE_TAG := v$(QWENCODE_VERSION)
JUPYTER_VERSION ?= $(shell cat JUPYTER_VERSION)
JUPYTER_TAG := v$(JUPYTER_VERSION)
NODERED_VERSION ?= $(shell cat NODERED_VERSION)
NODERED_TAG := v$(NODERED_VERSION)
N8N_VERSION ?= $(shell cat N8N_VERSION)
N8N_TAG := v$(N8N_VERSION)
GOOSE_VERSION ?= $(shell cat GOOSE_VERSION)
GOOSE_TAG := v$(GOOSE_VERSION)
HOLMES_VERSION ?= $(shell cat HOLMES_VERSION)
HOLMES_TAG := v$(HOLMES_VERSION)
BROWSERCODE_VERSION ?= $(shell cat BROWSERCODE_VERSION)
BROWSERCODE_TAG := v$(BROWSERCODE_VERSION)
OPENCODEREVIEW_VERSION ?= $(shell cat OPENCODEREVIEW_VERSION)
OPENCODEREVIEW_TAG := v$(OPENCODEREVIEW_VERSION)
ORCA_VERSION ?= $(shell cat ORCA_VERSION)
ORCA_TAG := v$(ORCA_VERSION)
OPENHANDS_VERSION ?= $(shell cat OPENHANDS_VERSION)
OPENHANDS_TAG := v$(OPENHANDS_VERSION)
PI_VERSION ?= $(shell cat PI_VERSION)
PI_TAG := v$(PI_VERSION)

test:
	go test -race ./cmd/... ./internal/...
	cd web && npm ci && npm run lint && npm run build

build:
	mkdir -p bin
	go build -o bin/agenthub ./cmd/agenthub
	go build -o bin/agenthub-operator ./cmd/operator

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg BASE_VERSION=$(BASE_VERSION) --build-arg LANGFLOW_VERSION=$(LANGFLOW_VERSION) --build-arg QWENCODE_VERSION=$(QWENCODE_VERSION) --build-arg JUPYTER_VERSION=$(JUPYTER_VERSION) --build-arg NODERED_VERSION=$(NODERED_VERSION) --build-arg N8N_VERSION=$(N8N_VERSION) --build-arg GOOSE_VERSION=$(GOOSE_VERSION) --build-arg HOLMES_VERSION=$(HOLMES_VERSION) --build-arg BROWSERCODE_VERSION=$(BROWSERCODE_VERSION) --build-arg OPENCODEREVIEW_VERSION=$(OPENCODEREVIEW_VERSION) --build-arg ORCA_VERSION=$(ORCA_VERSION) --build-arg OPENHANDS_VERSION=$(OPENHANDS_VERSION) --build-arg PI_VERSION=$(PI_VERSION) --build-arg COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) --build-arg BUILD_TIME=$$(date -u +%Y-%m-%dT%H:%M:%SZ) -t agenthub:$(TAG) .

image-base:
	docker build -f Dockerfile.base -t agenthub-base:$(BASE_TAG) .

image-langflow:
	docker build -f Dockerfile.langflow -t agenthub-langflow:$(LANGFLOW_TAG) .

image-qwencode:
	docker build -f Dockerfile.qwencode -t agenthub-qwencode:$(QWENCODE_TAG) .

# Built from the Qwen Code image, so that one has to exist first.
image-jupyter: image-qwencode
	docker build -f Dockerfile.jupyter --build-arg QWENCODE_IMAGE=agenthub-qwencode:$(QWENCODE_TAG) -t agenthub-jupyter:$(JUPYTER_TAG) .

image-goose:
	docker build -f Dockerfile.goose -t agenthub-goose:$(GOOSE_TAG) .

image-holmes:
	docker build -f Dockerfile.holmes -t agenthub-holmes:$(HOLMES_TAG) .

image-browsercode:
	docker build -f Dockerfile.browsercode -t agenthub-browsercode:$(BROWSERCODE_TAG) .

image-opencodereview:
	docker build -f Dockerfile.opencodereview -t agenthub-opencodereview:$(OPENCODEREVIEW_TAG) .

image-orca:
	docker build -f Dockerfile.orca -t agenthub-orca:$(ORCA_TAG) .

image-openhands:
	docker build -f Dockerfile.openhands --build-arg OPENHANDS_VERSION=$(OPENHANDS_VERSION) -t agenthub-openhands:$(OPENHANDS_TAG) .

image-pi:
	docker build -f Dockerfile.pi -t agenthub-pi:$(PI_TAG) .

image-nodered:
	docker build -f Dockerfile.nodered -t agenthub-nodered:$(NODERED_TAG) .

image-n8n:
	docker build -f Dockerfile.n8n -t agenthub-n8n:$(N8N_TAG) .

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

release-archives: image image-base image-langflow image-qwencode image-jupyter image-goose image-holmes image-browsercode image-opencodereview image-orca image-openhands image-pi image-nodered image-n8n
	mkdir -p release
	$(call package_image,agenthub:$(TAG),agenthub-$(TAG).tar.gz)
	$(call package_image,agenthub-base:$(BASE_TAG),agenthub-base-$(BASE_TAG).tar.gz)
	$(call package_image,agenthub-langflow:$(LANGFLOW_TAG),agenthub-langflow-$(LANGFLOW_TAG).tar.gz)
	$(call package_image,agenthub-qwencode:$(QWENCODE_TAG),agenthub-qwencode-$(QWENCODE_TAG).tar.gz)
	$(call package_image,agenthub-jupyter:$(JUPYTER_TAG),agenthub-jupyter-$(JUPYTER_TAG).tar.gz)
	$(call package_image,agenthub-goose:$(GOOSE_TAG),agenthub-goose-$(GOOSE_TAG).tar.gz)
	$(call package_image,agenthub-holmes:$(HOLMES_TAG),agenthub-holmes-$(HOLMES_TAG).tar.gz)
	$(call package_image,agenthub-browsercode:$(BROWSERCODE_TAG),agenthub-browsercode-$(BROWSERCODE_TAG).tar.gz)
	$(call package_image,agenthub-opencodereview:$(OPENCODEREVIEW_TAG),agenthub-opencodereview-$(OPENCODEREVIEW_TAG).tar.gz)
	$(call package_image,agenthub-orca:$(ORCA_TAG),agenthub-orca-$(ORCA_TAG).tar.gz)
	$(call package_image,agenthub-openhands:$(OPENHANDS_TAG),agenthub-openhands-$(OPENHANDS_TAG).tar.gz)
	$(call package_image,agenthub-pi:$(PI_TAG),agenthub-pi-$(PI_TAG).tar.gz)
	$(call package_image,agenthub-nodered:$(NODERED_TAG),agenthub-nodered-$(NODERED_TAG).tar.gz)
	$(call package_image,agenthub-n8n:$(N8N_TAG),agenthub-n8n-$(N8N_TAG).tar.gz)
	cd release && sha256sum -- agenthub-* > SHA256SUMS
	ls -lh release

define package_image
	docker save $(1) | gzip -9 | split -b $(RELEASE_CHUNK) - release/$(2).part-
	@parts=$$(ls release/$(2).part-* | wc -l); \
	if [ "$$parts" -eq 1 ]; then mv release/$(2).part-aa release/$(2); echo "packaged $(1) -> release/$(2)"; \
	else echo "packaged $(1) -> $$parts parts of release/$(2)"; fi
endef

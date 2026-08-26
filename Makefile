SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

.PHONY: test build image image-runtimes catalog-preflight catalog-image-ids validate release-archives

CATALOG := runtime-images.json
JQ ?= jq

VERSION ?= $(shell cat VERSION)
RELEASE_CHUNK ?= 1900M

# Pass Make values through the environment so neither command-line overrides nor
# catalog values are reparsed as shell source.
export AGENTHUB_MAKE_VERSION := $(VERSION)
export AGENTHUB_RELEASE_CHUNK := $(RELEASE_CHUNK)

test:
	go test -race ./cmd/... ./internal/...
	cd web && npm ci && npm run lint && npm run build

build:
	mkdir -p bin
	go build -o bin/agenthub ./cmd/agenthub
	go build -o bin/agenthub-operator ./cmd/operator

# Keep the catalog fail-closed before it reaches docker. The repository's Go
# validator performs deeper Dockerfile/source checks; these checks protect the
# dynamic Make recipes themselves and require dependency-first catalog order.
catalog-preflight:
	@command -v "$(JQ)" >/dev/null 2>&1 || { echo "jq is required to read $(CATALOG)" >&2; exit 1; }
	@"$(JQ)" -e '.schemaVersion == 1 and (.images | type == "array" and length > 0) and ([.images[].id] | length == (unique | length)) and ([.images[].image] | length == (unique | length)) and ([.images[].versionFile] | length == (unique | length)) and (all(.images[]; (.id | type == "string" and test("^[a-z0-9]+(-[a-z0-9]+)*$$")) and (.image | type == "string" and test("^agenthub-[a-z0-9]+(-[a-z0-9]+)*$$")) and (.image == ("agenthub-" + .id)) and (.versionFile | type == "string" and test("^[A-Z][A-Z0-9_]*_VERSION$$")) and (.dockerfile | type == "string" and test("^Dockerfile\\.[a-z0-9]+(-[a-z0-9]+)*$$")) and (.controlBuildArg | type == "string" and test("^[A-Z][A-Z0-9_]*_VERSION$$")) and (.controlBuildArg == .versionFile) and (.buildArgs | type == "object" and all(to_entries[]; (.key | test("^[A-Z][A-Z0-9_]*$$")) and (.value | type == "string" and (explode | all(.[]; . >= 32 and . != 127))))) and (.buildDependencies | type == "array" and all(.[]; type == "string" and test("^[a-z0-9]+(-[a-z0-9]+)*$$"))) and (.bundleDependencies | type == "array" and all(.[]; type == "string" and test("^[a-z0-9]+(-[a-z0-9]+)*$$"))))) and (all(.images[]; ((.id | ascii_downcase | contains("postgres")) | not) and ((.image | ascii_downcase | contains("postgres")) | not))) and (reduce .images[] as $$image ({seen: {}, valid: true}; .seen as $$seen | .valid = (.valid and ($$image.buildDependencies | all(.[]; $$seen[.] == true))) | .seen[$$image.id] = true) | .valid)' "$(CATALOG)" >/dev/null || { echo "invalid $(CATALOG): unsafe fields, duplicate entries, PostgreSQL image, or build-dependency-order violation" >&2; exit 1; }
	@while IFS=$$'\t' read -r version_file dockerfile; do \
		[[ -f "$$version_file" ]] || { echo "catalog version file does not exist: $$version_file" >&2; exit 1; }; \
		[[ -f "$$dockerfile" ]] || { echo "catalog Dockerfile does not exist: $$dockerfile" >&2; exit 1; }; \
	done < <("$(JQ)" -r '.images[] | [.versionFile, .dockerfile] | @tsv' "$(CATALOG)")

catalog-image-ids: catalog-preflight
	@"$(JQ)" -r '.images[].id' "$(CATALOG)"

image: catalog-preflight
	@mapfile -d '' -t catalog_versions < <("$(JQ)" -j '.images[] | .controlBuildArg, "\u0000", .versionFile, "\u0000"' "$(CATALOG)"); \
	(( $${#catalog_versions[@]} > 0 && $${#catalog_versions[@]} % 2 == 0 )) || { echo "invalid catalog control build arguments" >&2; exit 1; }; \
	build_command=(docker build --build-arg "VERSION=$$AGENTHUB_MAKE_VERSION"); \
	for ((index = 0; index < $${#catalog_versions[@]}; index += 2)); do \
		build_arg=$${catalog_versions[index]}; \
		version_file=$${catalog_versions[index + 1]}; \
		[[ -f "$$version_file" ]] || { echo "catalog version file does not exist: $$version_file" >&2; exit 1; }; \
		image_version=$$(<"$$version_file"); \
		[[ "$$image_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$ ]] || { echo "invalid version in $$version_file" >&2; exit 1; }; \
		build_command+=(--build-arg "$$build_arg=$$image_version"); \
	done; \
	commit=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown); \
	build_time=$$(date -u +%Y-%m-%dT%H:%M:%SZ); \
	build_command+=(--build-arg "COMMIT=$$commit" --build-arg "BUILD_TIME=$$build_time" -t "agenthub:v$$AGENTHUB_MAKE_VERSION" .); \
	"$${build_command[@]}"

# Pattern targets retain every existing `make image-<id>` convenience command
# while making additions/removals entirely catalog-driven.
image-%: export AGENTHUB_CATALOG_IMAGE_ID = $*
image-%: catalog-preflight
	+@if [[ "$${AGENTHUB_SKIP_IMAGE_DEPENDENCIES:-0}" != 1 ]]; then \
		id=$${AGENTHUB_CATALOG_IMAGE_ID:?missing catalog image id}; \
		[[ "$$id" =~ ^[a-z0-9]+(-[a-z0-9]+)*$$ ]] || { echo "invalid runtime image id: $$id" >&2; exit 1; }; \
		match_count=$$("$(JQ)" -r --arg id "$$id" '[.images[] | select(.id == $$id)] | length' "$(CATALOG)"); \
		[[ "$$match_count" == 1 ]] || { echo "unknown runtime image id: $$id" >&2; exit 1; }; \
		mapfile -t dependencies < <("$(JQ)" -r --arg id "$$id" '.images[] | select(.id == $$id) | .buildDependencies[]' "$(CATALOG)"); \
		for dependency in "$${dependencies[@]}"; do \
			$(MAKE) --no-print-directory "image-$$dependency"; \
		done; \
	fi
	@id=$${AGENTHUB_CATALOG_IMAGE_ID:?missing catalog image id}; \
	[[ "$$id" =~ ^[a-z0-9]+(-[a-z0-9]+)*$$ ]] || { echo "invalid runtime image id: $$id" >&2; exit 1; }; \
	match_count=$$("$(JQ)" -r --arg id "$$id" '[.images[] | select(.id == $$id)] | length' "$(CATALOG)"); \
	[[ "$$match_count" == 1 ]] || { echo "unknown runtime image id: $$id" >&2; exit 1; }; \
	mapfile -d '' -t fields < <("$(JQ)" -j --arg id "$$id" '.images[] | select(.id == $$id) | .image, "\u0000", .versionFile, "\u0000", .dockerfile, "\u0000"' "$(CATALOG)"); \
	(( $${#fields[@]} == 3 )) || { echo "invalid catalog record for $$id" >&2; exit 1; }; \
	image_name=$${fields[0]}; \
	version_file=$${fields[1]}; \
	dockerfile=$${fields[2]}; \
	[[ "$$image_name" =~ ^agenthub-[a-z0-9]+(-[a-z0-9]+)*$$ ]] || { echo "invalid image name for $$id" >&2; exit 1; }; \
	[[ "$$image_name" == "agenthub-$$id" ]] || { echo "catalog image name does not match id $$id" >&2; exit 1; }; \
	[[ "$$version_file" =~ ^[A-Z][A-Z0-9_]*_VERSION$$ && -f "$$version_file" ]] || { echo "invalid version file for $$id" >&2; exit 1; }; \
	[[ "$$dockerfile" =~ ^Dockerfile\.[a-z0-9]+(-[a-z0-9]+)*$$ && -f "$$dockerfile" ]] || { echo "invalid Dockerfile for $$id" >&2; exit 1; }; \
	image_version=$$(<"$$version_file"); \
	[[ "$$image_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$ ]] || { echo "invalid version in $$version_file" >&2; exit 1; }; \
	mapfile -d '' -t catalog_build_args < <("$(JQ)" -j --arg id "$$id" '.images[] | select(.id == $$id) | .buildArgs | to_entries | sort_by(.key)[] | .key, "\u0000", .value, "\u0000"' "$(CATALOG)"); \
	(( $${#catalog_build_args[@]} % 2 == 0 )) || { echo "invalid build arguments for $$id" >&2; exit 1; }; \
	build_command=(docker build -f "$$dockerfile"); \
	for ((index = 0; index < $${#catalog_build_args[@]}; index += 2)); do \
		build_arg=$${catalog_build_args[index]}; \
		build_value=$${catalog_build_args[index + 1]}; \
		[[ "$$build_arg" =~ ^[A-Z][A-Z0-9_]*$$ ]] || { echo "invalid build argument name for $$id" >&2; exit 1; }; \
		build_command+=(--build-arg "$$build_arg=$$build_value"); \
	done; \
	build_command+=(-t "$$image_name:v$$image_version" .); \
	"$${build_command[@]}"

image-runtimes: catalog-preflight
	+@mapfile -t image_ids < <("$(JQ)" -r '.images[].id' "$(CATALOG)"); \
	for id in "$${image_ids[@]}"; do \
		AGENTHUB_SKIP_IMAGE_DEPENDENCIES=1 $(MAKE) --no-print-directory "image-$$id"; \
	done

validate:
	kubectl kustomize deploy/kubernetes >/dev/null
	AGENTHUB_BOOTSTRAP_ADMIN=validation \
	AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=validation-password-only \
	AGENTHUB_ENCRYPTION_KEY=01234567890123456789012345678901 \
	docker compose config --quiet

# A GitHub release asset may not exceed 2 GiB. Archives are streamed through
# split so an oversized image lands as .part-aa/.part-ab/... ; anything that fits
# collapses back to a plain .tar.gz. Reassemble with `cat <name>.part-* > <name>`.
release-archives: image image-runtimes
	@[[ "$$AGENTHUB_RELEASE_CHUNK" =~ ^[1-9][0-9]*(K|M|G)?$$ ]] || { echo "invalid RELEASE_CHUNK: $$AGENTHUB_RELEASE_CHUNK" >&2; exit 1; }; \
	[[ "$$AGENTHUB_MAKE_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$ ]] || { echo "invalid VERSION: $$AGENTHUB_MAKE_VERSION" >&2; exit 1; }; \
	mkdir -p release; \
	produced_files=(); \
	package_image() { \
		local image_ref=$$1 archive_name=$$2; \
		shopt -s nullglob; \
		local previous_parts=("release/$$archive_name.part-"*); \
		rm -f -- "release/$$archive_name" "$${previous_parts[@]}"; \
		docker save "$$image_ref" | gzip -n -9 | split -b "$$AGENTHUB_RELEASE_CHUNK" - "release/$$archive_name.part-"; \
		local parts=("release/$$archive_name.part-"*); \
		(( $${#parts[@]} > 0 )) || { echo "packaging produced no archive for $$image_ref" >&2; return 1; }; \
		if (( $${#parts[@]} == 1 )); then \
			mv -- "$${parts[0]}" "release/$$archive_name"; \
			produced_files+=("release/$$archive_name"); \
			echo "packaged $$image_ref -> release/$$archive_name"; \
		else \
			produced_files+=("$${parts[@]}"); \
			echo "packaged $$image_ref -> $${#parts[@]} parts of release/$$archive_name"; \
		fi; \
	}; \
	package_image "agenthub:v$$AGENTHUB_MAKE_VERSION" "agenthub-v$$AGENTHUB_MAKE_VERSION.tar.gz"; \
	mapfile -d '' -t catalog_images < <("$(JQ)" -j '.images[] | .image, "\u0000", .versionFile, "\u0000"' "$(CATALOG)"); \
	(( $${#catalog_images[@]} % 2 == 0 )) || { echo "invalid catalog package entries" >&2; exit 1; }; \
	for ((index = 0; index < $${#catalog_images[@]}; index += 2)); do \
		image_name=$${catalog_images[index]}; \
		version_file=$${catalog_images[index + 1]}; \
		image_version=$$(<"$$version_file"); \
		[[ "$$image_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$ ]] || { echo "invalid version in $$version_file" >&2; exit 1; }; \
		package_image "$$image_name:v$$image_version" "$$image_name-v$$image_version.tar.gz"; \
	done; \
	checksum_names=(); \
	for path in "$${produced_files[@]}"; do checksum_names+=("$${path#release/}"); done; \
	(cd release && sha256sum -- "$${checksum_names[@]}" > SHA256SUMS); \
	ls -lh -- "$${produced_files[@]}" release/SHA256SUMS

#!/usr/bin/env bash
# Build, verify, and describe release images from runtime-images.json.
#
# The release workflow deliberately delegates all per-runtime decisions to this
# helper. Adding a runtime to the catalog is therefore enough to give it source
# guards, dependency ordering, smoke checks, packaging, and release notes.
set -euo pipefail

catalog="${AGENTHUB_IMAGE_CATALOG:-runtime-images.json}"
plan="${AGENTHUB_IMAGE_PLAN:-}"
release_index="${AGENTHUB_RELEASE_INDEX:-}"

die() {
  echo "::error::$*" >&2
  exit 1
}

require_file() {
  [ -f "$1" ] || die "required file does not exist: $1"
}

catalog_entry() {
  jq -ce --arg id "$1" '.images[] | select(.id == $id)' "$catalog"
}

plan_entry() {
  jq -ce --arg id "$1" '.images[] | select(.id == $id)' "$plan"
}

declare -Ag visit_state=()
declare -ag image_order=()

visit_image() {
  local id="$1" state="${visit_state[$1]:-new}" dependency
  case "$state" in
    visiting) die "runtime image dependency cycle reaches ${id}" ;;
    visited) return ;;
  esac
  visit_state["$id"]="visiting"
  while IFS= read -r dependency; do
    [ -n "$dependency" ] || continue
    if ! jq -e --arg id "$dependency" 'any(.images[]; .id == $id)' "$catalog" >/dev/null; then
      die "${id} depends on unknown catalog image ${dependency}"
    fi
    visit_image "$dependency"
  done < <(catalog_entry "$id" | jq -r '.buildDependencies[]')
  visit_state["$id"]="visited"
  image_order+=("$id")
}

resolve_image_order() {
  local id
  visit_state=()
  image_order=()
  while IFS= read -r id; do
    visit_image "$id"
  done < <(jq -r '.images[].id' "$catalog")
}

validate_catalog() {
  require_file "$catalog"
  jq -e '
    .schemaVersion == 1 and
    (.images | type == "array" and length > 0) and
    ([.images[].id] | length == (unique | length)) and
    ([.images[].image] | length == (unique | length)) and
    ([.images[].versionFile] | length == (unique | length)) and
    ([.images[].controlBuildArg] | length == (unique | length)) and
    (any(.images[]; .id == "base")) and
    all(.images[];
      (.id | test("^[a-z][a-z0-9]*$")) and
      (.image | test("^[a-z0-9]+([._-][a-z0-9]+)*$")) and
      (.versionFile | test("^[A-Z][A-Z0-9_]*_VERSION$|^BASE_VERSION$")) and
      (.dockerfile | type == "string" and length > 0) and
      (.sourcePaths | type == "array" and length > 0 and length == (unique | length)) and
      (.controlBuildArg | test("^[A-Z][A-Z0-9_]*$")) and
      (.buildArgs | type == "object") and
      (.buildDependencies | type == "array" and length == (unique | length)) and
      (.bundleDependencies | type == "array" and length == (unique | length)) and
      (.label | type == "string" and length > 0 and (test("[\\r\\n]") | not)) and
      (.note | type == "string" and length > 0 and (test("[\\r\\n]") | not)) and
      ((.health.kind == "command" and (.health.command | type == "array" and length > 0)) or
       (.health.kind == "http" and (.health.port >= 1 and .health.port <= 65535) and
        (.health.path | type == "string" and startswith("/")))) and
      ((.id + " " + .image) | ascii_downcase | contains("postgres") | not)
    )
  ' "$catalog" >/dev/null || die "runtime image catalog structure is invalid"

  local encoded entry id image version_file version dockerfile source dependency
  local dependency_arg dependency_image dependency_version dependency_reference
  while IFS= read -r encoded; do
    entry="$(printf '%s' "$encoded" | base64 --decode)"
    id="$(jq -r '.id' <<<"$entry")"
    image="$(jq -r '.image' <<<"$entry")"
    version_file="$(jq -r '.versionFile' <<<"$entry")"
    dockerfile="$(jq -r '.dockerfile' <<<"$entry")"
    [ "$image" = "agenthub-${id}" ] || die "catalog image ${id} must be named agenthub-${id}, got ${image}"
    require_file "$version_file"
    require_file "$dockerfile"
    version="$(<"$version_file")"
    [[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] ||
      die "${version_file} must contain a plain semantic version"
    while IFS= read -r source; do
      [ -e "$source" ] || die "${id} catalog source does not exist: ${source}"
    done < <(jq -r '.sourcePaths[]' <<<"$entry")
    if ! jq -e --arg path "$dockerfile" '.sourcePaths | index($path) != null' <<<"$entry" >/dev/null; then
      die "${id} sourcePaths does not include ${dockerfile}"
    fi

    # Every local COPY input must be watched. COPY --from is an external stage,
    # and variable/glob sources cannot be checked statically here.
    while IFS= read -r source; do
      [ -n "$source" ] || continue
      if ! jq -e --arg source "$source" '
        any(.sourcePaths[]; . as $candidate |
          ($candidate == $source) or
          ($candidate | startswith($source + "/")) or
          ($source | startswith($candidate + "/")))
      ' <<<"$entry" >/dev/null; then
        die "${id} Dockerfile copies ${source}, but sourcePaths does not watch it"
      fi
    done < <(awk '
      /^[[:space:]]*COPY[[:space:]]+/ && $0 !~ /--from=/ {
        for (i = 2; i < NF; i++) {
          value = $i
          sub(/^\.\//, "", value)
          sub(/\/$/, "", value)
          if (value !~ /[$*?\[{]/) print value
        }
      }
    ' "$dockerfile")

    while IFS= read -r dependency; do
      [ -n "$dependency" ] || continue
      [ "$dependency" != "$id" ] || die "${id} cannot depend on itself"
      if ! jq -e --arg id "$dependency" 'any(.images[]; .id == $id)' "$catalog" >/dev/null; then
        die "${id} names unknown dependency ${dependency}"
      fi
    done < <(jq -r '.buildDependencies[], .bundleDependencies[]' <<<"$entry")

    while IFS= read -r dependency; do
      [ -n "$dependency" ] || continue
      dependency_arg="$(printf '%s_IMAGE' "$dependency" | tr '[:lower:]-' '[:upper:]_')"
      dependency_image="$(catalog_entry "$dependency" | jq -r '.image')"
      dependency_version="$(tr -d '\r\n' < "$(catalog_entry "$dependency" | jq -r '.versionFile')")"
      dependency_reference="${dependency_image}:v${dependency_version}"
      if ! jq -e --arg key "$dependency_arg" --arg value "$dependency_reference" \
        '.buildArgs[$key] == $value' <<<"$entry" >/dev/null; then
        die "${id} dependency ${dependency} must be pinned by ${dependency_arg}=${dependency_reference}"
      fi
    done < <(jq -r '.buildDependencies[]' <<<"$entry")
  done < <(jq -r '.images[] | @base64' "$catalog")

  resolve_image_order

  # Dockerfile.base compiles one Go main package. Its catalog entry must match
  # the actual Linux/no-cgo local package graph exactly, not a hand-maintained
  # approximation that could silently reuse a stale multi-gigabyte archive.
  local actual_go catalog_go
  if ! actual_go="$(bash scripts/runtime-proxy-local-go-deps.sh)"; then
    die "could not resolve runtime-proxy's local Go dependencies"
  fi
  catalog_go="$(catalog_entry base | jq -r '.sourcePaths[] | select(startswith("cmd/") or startswith("internal/"))' | sort)"
  if [ "$(printf '%s\n' "$actual_go" | sed '/^$/d' | sort)" != "$catalog_go" ]; then
    echo "runtime-proxy dependency helper:" >&2
    printf '%s\n' "$actual_go" >&2
    echo "base catalog Go sources:" >&2
    printf '%s\n' "$catalog_go" >&2
    die "base catalog Go sources do not match runtime-proxy's compiled package graph"
  fi
  for source in go.mod go.sum; do
    if ! catalog_entry base | jq -e --arg source "$source" '.sourcePaths | index($source) != null' >/dev/null; then
      die "base sourcePaths must include ${source}"
    fi
  done
}

archive_source_release() {
  local archive="$1" tag current="${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}" candidate_lines
  local -a candidates=()
  # Resolve completeness once for the whole index. Running jq once per release
  # made a catalog plan quadratic in the number of images/releases.
  if ! candidate_lines="$(jq -r --arg archive "$archive" '
    def valid_asset:
      (.size | type == "number" and . > 0) and
      (.browser_download_url | type == "string" and startswith("https://")) and
      (((.digest // "") | test("^sha256:[0-9a-f]{64}$")) or
       ((.sha256 // "") | test("^[0-9a-f]{64}$")));
    .[] |
    select(.draft == false and .prerelease == false) |
    select(.tag_name | type == "string" and
      test("^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$")) |
    [.assets[]? | select(.name == $archive)] as $single |
    [.assets[]? | select(.name | startswith($archive + ".part-"))] as $split |
    select(
      (($single | length) == 1 and ($split | length) == 0 and
       ($single[0] | valid_asset)) or
      (($single | length) == 0 and ($split | length) > 1 and
       all($split[]; valid_asset) and
       ([$split[].name | .[($archive | length) + 6:]] | sort) as $parts |
       ([range(0; ($parts | length)) as $index |
         $parts[$index] ==
           ([97 + (($index / 26) | floor), 97 + ($index % 26)] | implode)] | all))) |
    .tag_name
  ' "$release_index" | sort -Vr)"; then
    die "could not resolve published archive evidence for ${archive}"
  fi
  if [ -n "$candidate_lines" ]; then
    mapfile -t candidates <<<"$candidate_lines"
  fi
  while IFS= read -r tag; do
    [ -n "$tag" ] || continue
    # Never source an archive from a future release, even if a release was
    # republished later and therefore has a newer published_at timestamp.
    if [ "$(printf '%s\n%s\n' "$tag" "$current" | sort -V | tail -1)" != "$current" ]; then
      continue
    fi
    printf '%s\n' "$tag"
    return 0
  done < <(printf '%s\n' "${candidates[@]}")
  return 1
}

previous_release_tag() {
  local current="${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}" candidate previous=""
  [[ "$current" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] ||
    die "current release tag is not stable semantic version: ${current}"
  while IFS= read -r candidate; do
    [[ "$candidate" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || continue
    [ "$candidate" != "$current" ] || continue
    # A stray/future tag must never become the source/version guard baseline.
    if [ "$(printf '%s\n%s\n' "$candidate" "$current" | sort -V | tail -1)" != "$current" ]; then
      continue
    fi
    if [ -z "$previous" ] ||
      [ "$(printf '%s\n%s\n' "$previous" "$candidate" | sort -V | tail -1)" = "$candidate" ]; then
      previous="$candidate"
    fi
  done
  printf '%s\n' "$previous"
}

base_module_inputs_changed() {
  local previous="$1" current="$2" modules moved module
  if git diff --quiet "$previous" "$current" -- go.mod go.sum; then
    return 1
  fi
  if ! modules="$(GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go list -mod=readonly -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/runtime-proxy | sort -u)"; then
    die "could not resolve runtime-proxy's module graph"
  fi
  if git diff -U0 "$previous" "$current" -- go.mod |
    grep -E '^[+-](go|toolchain|module|replace|exclude|retract)[[:space:]]' >/dev/null; then
    echo "the Go language/toolchain or module graph directive used by runtime-proxy changed" >&2
    return 0
  fi
  moved="$(git diff -U0 "$previous" "$current" -- go.mod go.sum |
    grep -E '^[+-][^+-]' |
    grep -oE '[A-Za-z0-9._-]+\.[A-Za-z]{2,}/[^[:space:]]+' |
    sed 's|/go.mod$||' | sort -u || true)"
  while IFS= read -r module; do
    [ -n "$module" ] || continue
    if printf '%s\n' "$modules" | grep -Fqx "$module"; then
      echo "dependency ${module} is compiled into the base image" >&2
      return 0
    fi
  done <<<"$moved"
  echo "go.mod/go.sum changed, but no moved module is compiled into the base image" >&2
  return 1
}

image_inputs_changed() {
  local id="$1" previous="$2" current="$3" source
  local -a sources=()
  while IFS= read -r source; do
    if [ "$id" = base ] && { [ "$source" = go.mod ] || [ "$source" = go.sum ]; }; then
      continue
    fi
    sources+=("$source")
  done < <(catalog_entry "$id" | jq -r '.sourcePaths[]')
  if ! git diff --quiet "$previous" "$current" -- "${sources[@]}"; then
    git diff --name-only "$previous" "$current" -- "${sources[@]}" >&2
    return 0
  fi
  if [ "$id" = base ] && base_module_inputs_changed "$previous" "$current"; then
    return 0
  fi

  # Catalog fields that influence docker build output are themselves source
  # inputs. The first catalog-based release is a migration: an older release
  # without the catalog still uses the explicit file diff above.
  if git cat-file -e "${previous}:runtime-images.json" 2>/dev/null; then
    local old_entry new_entry
    old_entry="$(git show "${previous}:runtime-images.json" | jq -Sc --arg id "$id" \
      '.images[] | select(.id == $id) | {image,versionFile,dockerfile,sourcePaths,buildArgs,buildDependencies}')"
    new_entry="$(catalog_entry "$id" | jq -Sc \
      '{image,versionFile,dockerfile,sourcePaths,buildArgs,buildDependencies}')"
    if [ -z "$old_entry" ] || [ "$old_entry" != "$new_entry" ]; then
      echo "${id} build metadata changed in runtime-images.json" >&2
      return 0
    fi
  fi
  return 1
}

mark_build() {
  local id="$1" dependency
  [ "${build_image[$id]:-false}" = true ] && return
  build_image["$id"]=true
  while IFS= read -r dependency; do
    [ -n "$dependency" ] || continue
    mark_build "$dependency"
  done < <(catalog_entry "$id" | jq -r '.buildDependencies[]')
}

create_plan() {
  validate_catalog
  require_file "$release_index"
  [ -n "$plan" ] || die "AGENTHUB_IMAGE_PLAN is required"
  local previous="${AGENTHUB_PREVIOUS_TAG:-}" current="${GITHUB_REF_NAME:-}" current_ref="${GITHUB_SHA:-}"
  [ -n "$current" ] || die "GITHUB_REF_NAME is required"
  [ -n "$current_ref" ] || current_ref="$current"
  jq -e 'type == "array" and all(.[]; has("tag_name") and has("assets"))' \
    "$release_index" >/dev/null || die "release index is not a GitHub release array"

  local images='[]' id entry version_file version previous_version archive
  local source_release source_changed publish reason published
  declare -gA build_image=()
  for id in "${image_order[@]}"; do
    entry="$(catalog_entry "$id")"
    version_file="$(jq -r '.versionFile' <<<"$entry")"
    version="$(tr -d '\r\n' < "$version_file")"
    archive="$(jq -r '.image' <<<"$entry")-v${version}.tar.gz"
    previous_version=""
    source_changed=false
    publish=false
    reason="unchanged"
    published=""
    if [ -z "$previous" ]; then
      source_changed=true
      publish=true
      reason="first-release"
    else
      previous_version="$(git show "${previous}:${version_file}" 2>/dev/null | tr -d '\r\n' || true)"
      if image_inputs_changed "$id" "$previous" "$current_ref"; then
        source_changed=true
      fi
      if [ "$source_changed" = true ] && [ "$version" = "$previous_version" ]; then
        die "${id} image inputs changed since ${previous}, but ${version_file} is still ${version}; bump it so the image gets a new tag"
      fi
      if [ "$version" != "$previous_version" ]; then
        publish=true
        reason="version-changed"
      fi
    fi
    if [ "$publish" = false ]; then
      published="$(archive_source_release "$archive" || true)"
      if [ -z "$published" ]; then
        publish=true
        reason="archive-missing"
      fi
    fi
    if [ "$publish" = true ]; then
      source_release="$current"
      build_image["$id"]=false
    else
      source_release="$published"
    fi
    images="$(jq -c --argjson entry "$entry" --arg version "$version" \
      --arg archive "$archive" --arg source "$source_release" --arg reason "$reason" \
      --argjson publish "$publish" \
      '. + [$entry + {version:$version, tag:("v" + $version), archive:$archive,
        sourceRelease:$source, reason:$reason, publish:$publish}]' <<<"$images")"
  done

  while IFS= read -r id; do
    mark_build "$id"
  done < <(jq -r '.[] | select(.publish) | .id' <<<"$images")
  local build_ids='[]'
  for id in "${image_order[@]}"; do
    if [ "${build_image[$id]:-false}" = true ]; then
      build_ids="$(jq -c --arg id "$id" '. + [$id]' <<<"$build_ids")"
    fi
  done
  images="$(jq -c --argjson ids "$build_ids" '
    map(. as $image | $image + {build: ($ids | index($image.id) != null)})
  ' <<<"$images")"

  local temporary="${plan}.partial"
  jq -n --arg current "$current" --arg previous "$previous" --argjson images "$images" \
    '{schemaVersion:1,currentTag:$current,previousTag:$previous,images:$images}' > "$temporary"
  jq -e 'all(.images[]; (.publish | type == "boolean") and (.build | type == "boolean"))' \
    "$temporary" >/dev/null
  mv "$temporary" "$plan"
  jq -r '.images[] | "\(.id): v\(.version) publish=\(.publish) build=\(.build) source=\(.sourceRelease) reason=\(.reason)"' "$plan"
}

smoke_image() {
  local entry="$1" reference="$2" kind
  kind="$(jq -r '.health.kind' <<<"$entry")"
  case "$kind" in
    command)
      local -a command=()
      mapfile -t command < <(jq -r '.health.command[]' <<<"$entry")
      [ "${#command[@]}" -gt 0 ] || die "${reference} has an empty command health check"
      docker run --rm --entrypoint "${command[0]}" "$reference" "${command[@]:1}"
      ;;
    http)
      local port path container url healthy=false attempt
      port="$(jq -r '.health.port' <<<"$entry")"
      path="$(jq -r '.health.path' <<<"$entry")"
      # Runtimes share a Pod network namespace in production and may correctly
      # bind loopback only. Host networking gives the smoke check the same
      # reachability without pulling a second curl image into the runner.
      container="$(docker run -d --network host "$reference")"
      url="http://127.0.0.1:${port}${path}"
      for attempt in $(seq 1 60); do
        if ! docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null | grep -qx true; then
          break
        fi
        if curl --fail --silent --max-time 5 "$url" >/dev/null; then
          healthy=true
          break
        fi
        sleep 2
      done
      if [ "$healthy" != true ]; then
        docker logs "$container" >&2 || true
        docker rm -f "$container" >/dev/null 2>&1 || true
        die "${reference} did not pass HTTP health check on port ${port}${path}"
      fi
      docker rm -f "$container" >/dev/null
      ;;
    *) die "${reference} has unsupported health kind ${kind}" ;;
  esac
}

build_images() {
  validate_catalog
  require_file "$plan"
  local release_version="${RELEASE_VERSION:?RELEASE_VERSION is required}"
  local release_tag="${RELEASE_TAG:?RELEASE_TAG is required}"
  local commit="${GITHUB_SHA:?GITHUB_SHA is required}" epoch build_time
  epoch="$(git show -s --format=%ct "$commit")"
  [[ "$epoch" =~ ^[0-9]+$ ]] || die "could not obtain commit timestamp for ${commit}"
  build_time="$(date -u --date="@${epoch}" +%Y-%m-%dT%H:%M:%SZ)"
  local -a args=(
    --build-arg "VERSION=${release_version}"
    --build-arg "COMMIT=${commit}"
    --build-arg "BUILD_TIME=${build_time}"
  )
  local id entry arg version
  for id in "${image_order[@]}"; do
    entry="$(plan_entry "$id")"
    arg="$(jq -r '.controlBuildArg' <<<"$entry")"
    version="$(jq -r '.version' <<<"$entry")"
    args+=(--build-arg "${arg}=${version}")
  done
  docker build "${args[@]}" -t "agenthub:${release_tag}" .

  local info
  info="$(docker run --rm "agenthub:${release_tag}" version --json)"
  if ! jq -e --arg version "$release_version" --arg commit "$commit" \
    '.version == $version and .commit == $commit' <<<"$info" >/dev/null; then
    echo "$info" >&2
    die "control image version/commit does not match ${release_version}/${commit}"
  fi

  local built_file="${AGENTHUB_BUILT_IMAGES:?AGENTHUB_BUILT_IMAGES is required}"
  printf '%s\n' "agenthub:${release_tag}" > "$built_file"
  local dockerfile image tag dependency dependency_arg dependency_reference encoded key value
  for id in "${image_order[@]}"; do
    entry="$(plan_entry "$id")"
    [ "$(jq -r '.build' <<<"$entry")" = true ] || continue
    dockerfile="$(jq -r '.dockerfile' <<<"$entry")"
    image="$(jq -r '.image' <<<"$entry")"
    tag="$(jq -r '.tag' <<<"$entry")"
    args=()
    while IFS= read -r encoded; do
      key="$(printf '%s' "$encoded" | base64 --decode | jq -r '.key')"
      value="$(printf '%s' "$encoded" | base64 --decode | jq -r '.value')"
      args+=(--build-arg "${key}=${value}")
    done < <(jq -r '.buildArgs | to_entries[] | @base64' <<<"$entry")
    # A dependency reference is derived from the dependency's current plan,
    # after that dependency has been built in topological order.
    while IFS= read -r dependency; do
      dependency_arg="$(printf '%s_IMAGE' "$dependency" | tr '[:lower:]-' '[:upper:]_')"
      dependency_reference="$(plan_entry "$dependency" | jq -r '.image + ":" + .tag')"
      args+=(--build-arg "${dependency_arg}=${dependency_reference}")
    done < <(jq -r '.buildDependencies[]' <<<"$entry")
    docker build -f "$dockerfile" "${args[@]}" -t "${image}:${tag}" .
    printf '%s\n' "${image}:${tag}" >> "$built_file"
    if [ "$(jq -r '.publish' <<<"$entry")" = true ]; then
      smoke_image "$entry" "${image}:${tag}"
    fi
  done
}

package_image() {
  local reference="$1" archive="$2" output_directory="$3"
  local destination="${output_directory}/${archive}"
  if [ -e "$destination" ] || compgen -G "${destination}.part-*" >/dev/null; then
    die "refusing to overwrite existing archive ${destination}"
  fi
  docker save "$reference" | gzip -n -9 | split -b "${RELEASE_CHUNK:-1900M}" - "${destination}.part-"
  local -a parts=("${destination}".part-*)
  if [ "${#parts[@]}" -eq 1 ]; then
    mv "${parts[0]}" "$destination"
    echo "packaged ${reference} -> ${destination}"
  else
    echo "packaged ${reference} -> ${#parts[@]} parts of ${destination}"
  fi
  docker image rm "$reference" >/dev/null 2>&1 || true
}

package_images() {
  require_file "$plan"
  local output_directory="${RELEASE_DIRECTORY:-release}"
  local release_tag="${RELEASE_TAG:?RELEASE_TAG is required}"
  mkdir -p "$output_directory"
  package_image "agenthub:${release_tag}" "agenthub-${release_tag}.tar.gz" "$output_directory"
  local encoded entry image tag archive
  while IFS= read -r encoded; do
    entry="$(printf '%s' "$encoded" | base64 --decode)"
    image="$(jq -r '.image' <<<"$entry")"
    tag="$(jq -r '.tag' <<<"$entry")"
    archive="$(jq -r '.archive' <<<"$entry")"
    package_image "${image}:${tag}" "$archive" "$output_directory"
  done < <(jq -r '.images[] | select(.publish) | @base64' "$plan")
  while IFS= read -r encoded; do
    entry="$(printf '%s' "$encoded" | base64 --decode)"
    image="$(jq -r '.image + ":" + .tag' <<<"$entry")"
    docker image rm "$image" >/dev/null 2>&1 || true
  done < <(jq -r '.images[] | select(.build and (.publish | not)) | @base64' "$plan")
}

verify_archive() {
  local directory="$1" archive="$2"
  if [ -f "${directory}/${archive}" ]; then
    gzip -t "${directory}/${archive}"
    echo "verified ${archive}"
    return
  fi
  local -a parts=("${directory}/${archive}".part-*)
  [ -e "${parts[0]}" ] || die "release archive is missing: ${archive}"
  local index expected
  for index in "${!parts[@]}"; do
    expected="${archive}.part-$(awk -v index="$index" 'BEGIN {
      printf "%c%c", 97 + int(index / 26), 97 + (index % 26)
    }')"
    [ "$(basename "${parts[$index]}")" = "$expected" ] ||
      die "archive ${archive} has non-contiguous part $(basename "${parts[$index]}"); expected ${expected}"
  done
  cat "${parts[@]}" | gzip -t
  echo "verified ${#parts[@]} parts of ${archive}"
}

verify_images() {
  require_file "$plan"
  local directory="${RELEASE_DIRECTORY:-release}"
  local release_tag="${RELEASE_TAG:?RELEASE_TAG is required}"
  (cd "$directory" && sha256sum -c SHA256SUMS)
  verify_archive "$directory" "agenthub-${release_tag}.tar.gz"
  local archive
  while IFS= read -r archive; do
    verify_archive "$directory" "$archive"
  done < <(jq -r '.images[] | select(.publish) | .archive' "$plan")
  local oversized
  oversized="$(find "$directory" -maxdepth 1 -type f -size +2047M -print)"
  [ -z "$oversized" ] || die "release asset exceeds GitHub's 2 GiB limit: ${oversized}"
}

release_notes() {
  require_file "$plan"
  local release_tag="${RELEASE_TAG:?RELEASE_TAG is required}" encoded entry label image tag note publish source status pins
  echo "## 주요 변경"
  echo
  echo '- PostgreSQL 17은 Docker 이미지/오프라인 번들에 **포함되지 않습니다**. 별도로 운영하는 PostgreSQL과 `AGENTHUB_POSTGRES_DSN` 연결이 필수입니다.'
  echo '- `offline-bundle.json`과 `agenthub-offline-linux-amd64`가 런타임 선택(`--runtime`, `--all-runtimes`, `--no-runtimes`), 검증 다운로드, 분할 archive 로드를 제공합니다.'
  echo '- `runtime-images.json` 카탈로그가 런타임 종류, 버전, 빌드 입력, 의존성, archive와 상태 점검을 빠짐없이 연결합니다.'
  echo '- 게시 이미지마다 SPDX JSON SBOM을 제공하고, `SHA256SUMS`는 GitHub OIDC/Sigstore attestation bundle로 증명합니다.'
  echo '- 제어 이미지의 `version --json` 결과(버전/commit)와 게시 런타임의 command/HTTP 상태를 이미지 빌드 직후 검사합니다.'
  pins="$(jq -r '[.images[] |
    select(.reason == "version-changed" and (.id == "base" or .id == "n8n" or .id == "orca")) |
    "`" + .image + ":" + .tag + "`"] | join(", ")' "$plan")"
  if [ -n "$pins" ]; then
    echo "- 재현 가능한 입력을 위해 base/n8n/orca 핀을 갱신했습니다: ${pins}"
  fi
  echo
  echo "AgentHub 제어 이미지: \`agenthub:${release_tag}\` — 이번 릴리즈에 게시됩니다."
  echo
  echo "### 런타임 이미지"
  echo
  echo '| 런타임 | 이미지 | 상태 | 용도 |'
  echo '| --- | --- | --- | --- |'
  while IFS= read -r encoded; do
    entry="$(printf '%s' "$encoded" | base64 --decode)"
    label="$(jq -r '.label' <<<"$entry")"
    image="$(jq -r '.image' <<<"$entry")"
    tag="$(jq -r '.tag' <<<"$entry")"
    note="$(jq -r '.note | gsub("\\|"; "\\\\|")' <<<"$entry")"
    publish="$(jq -r '.publish' <<<"$entry")"
    source="$(jq -r '.sourceRelease' <<<"$entry")"
    if [ "$publish" = true ]; then
      status="이번 릴리즈 자산(큰 이미지는 분할 게시)"
    else
      status="변경 없음 — ${source} 자산 재사용"
    fi
    printf '| %s | `%s:%s` | %s | %s |\n' "$label" "$image" "$tag" "$status" "$note"
  done < <(jq -r '.images[] | @base64' "$plan")
  echo
  echo '각 자산의 실제 원본 릴리즈와 분할 파일/체크섬은 `offline-bundle.json`에 기록됩니다.'
}

case "${1:-}" in
  validate) validate_catalog ;;
  plan) create_plan ;;
  build) build_images ;;
  package) package_images ;;
  verify) verify_images ;;
  notes) release_notes ;;
  archive-source)
    [ "$#" -eq 2 ] || die "usage: $0 archive-source <logical-archive>"
    require_file "$release_index"
    archive_source_release "$2"
    ;;
  previous-tag) previous_release_tag ;;
  *) die "usage: $0 <validate|plan|build|package|verify|notes|archive-source|previous-tag>" ;;
esac

#!/usr/bin/env bash
set -euo pipefail

# Dockerfile.base builds the proxy for Linux without cgo. Resolve the package
# graph under those same constraints so release change detection watches every
# local package that can affect the binary, without rebuilding the large base
# image for unrelated control-plane packages.
repo_root="$(git rev-parse --show-toplevel)"
cd "${repo_root}"

if ! packages="$(GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go list -mod=readonly -deps \
  -f '{{if and .Module .Module.Main}}{{.Module.Path}} {{.ImportPath}}{{end}}' \
  ./cmd/runtime-proxy)"; then
  echo "failed to resolve runtime-proxy dependencies for the base image" >&2
  exit 1
fi

paths=()
while read -r module_path import_path extra; do
  if [ -z "${module_path:-}" ]; then
    continue
  fi
  if [ -n "${extra:-}" ] || [ -z "${import_path:-}" ]; then
    echo "unexpected go list output: ${module_path} ${import_path:-} ${extra:-}" >&2
    exit 1
  fi
  if [ "${import_path}" = "${module_path}" ]; then
    path="."
  elif [[ "${import_path}" == "${module_path}/"* ]]; then
    path="${import_path#"${module_path}/"}"
  else
    echo "main-module package ${import_path} is outside ${module_path}" >&2
    exit 1
  fi
  if [ ! -d "${path}" ]; then
    echo "runtime-proxy dependency path does not exist: ${path}" >&2
    exit 1
  fi
  paths+=("${path}")
done <<< "${packages}"

if [ "${#paths[@]}" -eq 0 ]; then
  echo "go list returned no main-module dependencies for runtime-proxy" >&2
  exit 1
fi

printf '%s\n' "${paths[@]}" | sort -u

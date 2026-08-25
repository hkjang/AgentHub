#!/usr/bin/env bash
set -euo pipefail

archive="${1:?archive name is required}"
assets_file="${2:?release asset index is required}"

# A small image is one archive. Images above GitHub's 2 GiB asset limit are
# uploaded as archive.part-aa, archive.part-ab, and so on. This lookup answers
# whether a matching published asset exists; the release job separately checks
# every archive and checksum before it uploads them.
while IFS= read -r asset; do
  case "${asset}" in
    "${archive}"|"${archive}".part-*) exit 0 ;;
  esac
done < "${assets_file}"

exit 1

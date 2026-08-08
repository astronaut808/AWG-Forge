#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tag=$(awk '/^## v[0-9]+\.[0-9]+\.[0-9]+ - / { print $2; exit }' "$root/CHANGELOG.md")

if [[ -z "$tag" ]]; then
  echo "FAIL release workflow: no released version found in CHANGELOG.md" >&2
  exit 1
fi

notes=$(awk -v tag="$tag" '
  index($0, "## " tag " - ") == 1 { found = 1; next }
  found && /^## / { exit }
  found { print }
' "$root/CHANGELOG.md")

if [[ -z "${notes//[[:space:]]/}" ]]; then
  echo "FAIL release workflow: $tag has no release notes" >&2
  exit 1
fi

echo "OK   release workflow extracts notes for $tag"

#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || exit 1
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
awk -v tag="$tag" '
  index($0, "## " tag " - ") == 1 { found = 1; next }
  found && /^## / { exit }
  found {
    print
    if ($0 ~ /[^[:space:]]/) content = 1
  }
  END { if (!content) exit 1 }
' "$root/CHANGELOG.md"

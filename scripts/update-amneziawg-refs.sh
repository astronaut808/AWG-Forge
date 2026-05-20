#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
refs_file="$root/build/amneziawg.refs"

go_ref="$(git ls-remote https://github.com/amnezia-vpn/amneziawg-go HEAD | awk '{print $1}')"
tools_ref="$(git ls-remote https://github.com/amnezia-vpn/amneziawg-tools HEAD | awk '{print $1}')"

if [[ -z "$go_ref" || -z "$tools_ref" ]]; then
  echo "failed to resolve AmneziaWG upstream refs" >&2
  exit 1
fi

tmp="$(mktemp)"
cat > "$tmp" <<EOF
AMNEZIAWG_GO_REF=$go_ref
AMNEZIAWG_TOOLS_REF=$tools_ref
EOF

if cmp -s "$refs_file" "$tmp"; then
  echo "AmneziaWG refs are already current."
  rm -f "$tmp"
  exit 0
fi

mv "$tmp" "$refs_file"
echo "Updated $refs_file"
git diff -- "$refs_file"

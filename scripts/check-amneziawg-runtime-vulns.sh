#!/usr/bin/env bash
set -euo pipefail
export GOWORK=off

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
refs_file="$root/build/amneziawg.refs"
govulncheck_version=${GOVULNCHECK_VERSION:-v1.9.0}
upstream=https://github.com/amnezia-vpn/amneziawg-go

go_ref=$(awk -F= '$1 == "AMNEZIAWG_GO_REF" { print $2 }' "$refs_file")
if [[ ! "$go_ref" =~ ^[0-9a-f]{40}$ ]]; then
  echo "AMNEZIAWG_GO_REF must be one full lowercase commit SHA" >&2
  exit 1
fi

work=$(mktemp -d)
cleanup() {
  rm -rf "$work"
}
trap cleanup EXIT

git -C "$work" init --quiet
git -C "$work" remote add origin "$upstream"
git -C "$work" fetch --quiet --depth=1 --no-tags origin "$go_ref"
git -C "$work" checkout --quiet --detach FETCH_HEAD

resolved_ref=$(git -C "$work" rev-parse HEAD)
if [[ "$resolved_ref" != "$go_ref" ]]; then
  echo "fetched AmneziaWG ref does not match build/amneziawg.refs" >&2
  exit 1
fi

(
  bash "$root/scripts/prepare-amneziawg-runtime.sh" "$work"
  cd "$work"
  go run "golang.org/x/vuln/cmd/govulncheck@${govulncheck_version}" .
)

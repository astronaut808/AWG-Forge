#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_dir="$(cd "${1:?usage: prepare-amneziawg-runtime.sh SOURCE_DIR}" && pwd)"
go_ref=$(awk -F= '$1 == "AMNEZIAWG_GO_REF" { print $2 }' "$root/build/amneziawg.refs")

if [[ ! "$go_ref" =~ ^[0-9a-f]{40}$ ]] || [[ "$(git -C "$source_dir" rev-parse HEAD)" != "$go_ref" ]]; then
  echo "runtime source does not match the pinned AmneziaWG commit" >&2
  exit 1
fi
git -C "$source_dir" diff --quiet HEAD --
if [[ -n "$(git -C "$source_dir" ls-files --others --exclude-standard)" ]]; then
  echo "runtime preparation requires a clean upstream checkout" >&2
  exit 1
fi
git -C "$source_dir" apply --check "$root/build/patches/amneziawg-go-dependencies.patch"
git -C "$source_dir" apply "$root/build/patches/amneziawg-go-dependencies.patch"

cd "$source_dir"
export GOWORK=off
module_hashes=$(git hash-object go.mod go.sum)
go mod download
go mod verify
if [[ "$module_hashes" != "$(git hash-object go.mod go.sum)" ]]; then
  echo "runtime dependency manifest changed during download" >&2
  exit 1
fi

# These unrelated upstream SDKs are not shipped. A newly reachable transport
# requires a separate API/security review rather than an implicit SDK fork.
for arch in amd64 arm64; do
  modules=$(GOOS=linux GOARCH="$arch" go list -mod=readonly -deps -f '{{if .Module}}{{.Module.Path}}@{{.Module.Version}}{{end}}' . | sort -u)
  if grep -Eq '^(golang.getoutline.org/|github.com/quic-go/|gvisor.dev/gvisor)' <<< "$modules"; then
    echo "runtime imports an unaudited SDK on linux/$arch" >&2
    exit 1
  fi
  for dependency in golang.org/x/crypto@v0.58.0 golang.org/x/net@v0.62.0 golang.org/x/sys@v0.49.0; do
    if ! grep -Fxq "$dependency" <<< "$modules"; then
      echo "unexpected runtime dependency version on linux/$arch: $dependency" >&2
      exit 1
    fi
  done
done
echo "OK pinned runtime dependency patch, integrity and Linux import closure"

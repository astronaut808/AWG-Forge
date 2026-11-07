#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tag=$(awk '/^## v[0-9]+\.[0-9]+\.[0-9]+ - / { print $2; exit }' "$root/CHANGELOG.md")

if [[ -z "$tag" ]]; then
  echo "FAIL release workflow: no released version found in CHANGELOG.md" >&2
  exit 1
fi

notes=$(bash "$root/scripts/release-notes.sh" "$tag")

if [[ -z "${notes//[[:space:]]/}" ]] || grep -q '^## ' <<<"$notes"; then
  echo "FAIL release workflow: $tag has no release notes" >&2
  exit 1
fi
if bash "$root/scripts/release-notes.sh" 'v999999.0.0' >/dev/null; then
  echo "FAIL release workflow: missing changelog section was accepted" >&2
  exit 1
fi

job_block() {
  awk -v name="$2" '
    $0 == "  " name ":" { found = 1; print; next }
    found && /^  [[:alnum:]_-]+:/ { exit }
    found { print }
  ' "$1"
}

require_line() {
  if ! grep -Eq "$1" <<<"$2"; then
    printf 'FAIL release workflow gate: %s\n' "$3" >&2
    exit 1
  fi
}

docker_workflow="$root/.github/workflows/docker.yml"
release_workflow="$root/.github/workflows/release.yml"
validate=$(job_block "$docker_workflow" validate)
publish=$(job_block "$docker_workflow" publish)
image=$(job_block "$release_workflow" image)
release=$(job_block "$release_workflow" publish)
tag_check=$(job_block "$release_workflow" validate-tag)

require_line '^    needs: validate$' "$publish" 'image publication requires validation'
require_line "^    if: github.event_name == 'push'$" "$publish" 'PR and manual dispatch cannot publish'
require_line '^      - name: Smoke test image$' "$validate" 'API smoke must run'
require_line '^      - name: Test encrypted traffic and restart for all profiles$' "$validate" 'runtime smoke must run'
require_line 'trivy image .*--pkg-types os,library .*--exit-code 1 awg-forge:ci$' "$validate" 'OS and library scan must block'
require_line '^          platforms: linux/amd64$' "$validate" 'supported architecture must be built'
require_line 'docker image save --output verified-image.tar awg-forge:ci$' "$validate" 'transfer the tested image'
require_line 'docker image load --input verified/verified-image.tar$' "$publish" 'load the tested image'
require_line 'test .*docker image inspect.* = "\$IMAGE_ID"$' "$publish" 'verify image identity after transfer'
require_line '^    needs: validate-tag$' "$image" 'image publication requires tag checks'
require_line '^    uses: ./.github/workflows/docker.yml$' "$image" 'release uses the image validation workflow'
require_line 'git merge-base --is-ancestor HEAD refs/remotes/origin/master$' "$tag_check" 'tag must belong to master'
require_line '^    needs: image$' "$release" 'GitHub Release follows the image'
require_line '^  group: docker-' "$(cat "$docker_workflow")" 'reusable workflow has separate concurrency'
if grep -Eq '^    if:|packages: write|contents: write' <<<"$validate" ||
   grep -Eq 'build-push-action|docker (build|buildx)' <<<"$publish"; then
  echo 'FAIL release workflow: validation is conditional or privileged, or publication rebuilds the image' >&2
  exit 1
fi

echo "OK   release workflow extracts notes for $tag"
echo 'OK   release workflow gates passed'

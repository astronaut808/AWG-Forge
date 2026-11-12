#!/usr/bin/env bash
set -euo pipefail

image=${IMAGE:-awg-forge:local}
container="awg-forge-smoke-${RANDOM}-${RANDOM}"
volume="${container}-data"
cookie_jar=$(mktemp)

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  rm -f "$cookie_jar"
}
trap cleanup EXIT

wait_for_ui() {
  local mapping

  for _ in $(seq 1 30); do
    mapping=$(docker port "$container" 51821/tcp 2>/dev/null | head -n 1 || true)
    port=${mapping##*:}
    if [[ -n "$mapping" ]] && curl -fsS "http://127.0.0.1:${port}/" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  return 1
}

docker volume create "$volume" >/dev/null
docker run --rm \
  -v "$volume:/etc/awg-forge" \
  -e APPLY_CONFIG=false \
  -e DATABASE_MODE=off \
  "$image" init \
  --server-host 127.0.0.1 \
  --external-interface eth0 \
  --profile awg_legacy_1_0 \
  --tunnel-name awg0 \
  --listen-port 51820 \
  --ipv4-subnet 10.8.0.0/24

if output=$(docker run --rm --network none \
  -v "$volume:/etc/awg-forge" -e APPLY_CONFIG=false \
  -e WEBUI_HOST=192.0.2.10 "$image" 2>&1) ||
   [[ "$output" != *"PASSWORD is required"* ]]; then
  echo "docker smoke: passwordless non-loopback configuration was not rejected" >&2
  exit 1
fi

docker run -d --name "$container" \
  -p 127.0.0.1::51821 \
  -v "$volume:/etc/awg-forge" \
  -e WEBUI_HOST=0.0.0.0 \
  -e WEBUI_PORT=51821 \
  -e PASSWORD=ci-smoke-password \
  -e SESSION_SECRET=ci-smoke-session-secret-32-bytes \
  -e APPLY_CONFIG=false \
  -e DATABASE_MODE=off \
  "$image" >/dev/null

if ! wait_for_ui; then
  echo "docker smoke: Web UI did not become ready" >&2
  docker logs "$container" >&2
  exit 1
fi

test "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/api/state")" = 401

curl -fsS \
  -c "$cookie_jar" \
  -H 'Content-Type: application/json' \
  --data '{"password":"ci-smoke-password"}' \
  "http://127.0.0.1:${port}/api/login" >/dev/null
state=$(curl -fsS -b "$cookie_jar" "http://127.0.0.1:${port}/api/state")
grep -q '"interface":"awg0"' <<<"$state"

for binary in awg-forge awg awg-quick amneziawg-go; do
  docker exec "$container" sh -c 'command -v "$1"' sh "$binary" >/dev/null
done
docker exec "$container" awg-quick strip /etc/awg-forge/tunnels/awg0/server.conf >/dev/null

docker restart "$container" >/dev/null
if ! wait_for_ui; then
  echo "docker smoke: Web UI did not recover after restart" >&2
  docker logs "$container" >&2
  exit 1
fi
state=$(curl -fsS -b "$cookie_jar" "http://127.0.0.1:${port}/api/state")
grep -q '"interface":"awg0"' <<<"$state"
docker exec "$container" awg-quick strip /etc/awg-forge/tunnels/awg0/server.conf >/dev/null

echo "OK docker image startup, API, runtime binaries, config parser, and restart persistence"

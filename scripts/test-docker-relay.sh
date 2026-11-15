#!/usr/bin/env bash
set -euo pipefail

image="${IMAGE:-awg-forge:local}"
source "$(dirname "${BASH_SOURCE[0]}")/../install.sh"
if [[ "${RELAY_REQUIRE_SELINUX:-0}" == 1 ]]; then
  if [[ ! -r /sys/fs/selinux/enforce || "$(< /sys/fs/selinux/enforce)" != 1 ]] ||
    ! docker info --format '{{json .SecurityOptions}}' | jq -e 'any(.[]; . == "name=selinux")' >/dev/null; then
    echo "relay test requires SELinux enforcing and a local Docker daemon with SELinux enabled" >&2
    exit 1
  fi
fi
suffix="$$-$RANDOM"
network="forge-relay-test-$suffix"
relay="forge-relay-node-$suffix"
target="forge-relay-exit-$suffix"
relay_service="forge-relay-service-$suffix"
client="forge-relay-client-$suffix"
no_forward="forge-relay-no-forward-$suffix"
compose_dir="$(mktemp -d)"
cleanup() {
  docker rm -fv "$relay_service" "$client" "$relay" "$target" "$no_forward" >/dev/null 2>&1 || :
  docker network rm "$network" >/dev/null 2>&1 || :
  rm -f -- "$compose_dir/current.yaml" "$compose_dir/next.yaml"
  if [[ -d "$compose_dir" ]]; then
    rmdir -- "$compose_dir/runtime" "$compose_dir"
  fi
}
trap cleanup EXIT
mkdir -m 0700 "$compose_dir/runtime"

docker network create "$network" >/dev/null
docker run -d --name "$target" --network "$network" --entrypoint perl "$image" -MIO::Socket::INET -e '
  $socket = IO::Socket::INET->new(LocalPort => $ARGV[0], Proto => "udp") or die $!;
  while (1) {
    $peer = $socket->recv($payload, 2048) or die $!;
    $socket->send("$ARGV[1]:$payload", 0, $peer) or die $!;
  }' 40123 old >/dev/null
docker run -d --name "$relay" --network "$network" --cap-add NET_ADMIN \
  --sysctl net.ipv4.ip_forward=1 --entrypoint sleep "$image" infinity >/dev/null
docker run -d --name "$client" --network "$network" --entrypoint sleep "$image" infinity >/dev/null
target_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$target")"
relay_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$relay")"
args=(0123456789abcdef 40124 "$target_ip" 40123 "$relay_ip" eth0)
run_relay() {
  docker run --rm --network "container:$relay" --cap-drop ALL --cap-add NET_ADMIN \
    --security-opt no-new-privileges --read-only --tmpfs /run --tmpfs /etc/awg-forge \
    "$image" relay "$@"
}

wait_for_rules() {
  for _ in {1..10}; do
    if run_relay status "$@" 2>/dev/null; then return 0; fi
    sleep 1
  done
  run_relay status "$@"
}

docker exec "$relay" iptables -P FORWARD DROP
docker exec "$relay" iptables -A FORWARD -p tcp --dport 20001 -j ACCEPT
# Two processes sharing the network namespace must contend for the same lock.
docker exec -d "$relay" flock -x /proc/self/ns/net sleep 5
sleep 1
if docker run --rm --network "container:$relay" --entrypoint flock "$image" -n /proc/self/ns/net true; then
  echo "network namespace lock did not serialize containers" >&2
  exit 1
fi
docker exec -d "$relay" perl -MIO::Socket::INET -e '
  $socket = IO::Socket::INET->new(LocalPort => 40124, PeerAddr => $ARGV[0], PeerPort => 40123, Proto => "udp") or die $!;
  sleep 8;
' "$target_ip"
for _ in {1..5}; do
  if [[ -n "$(docker exec "$relay" ss -Haun 'sport = :40124')" ]]; then break; fi
  sleep 1
done
if run_relay serve "${args[@]}"; then
  echo "connected UDP socket conflict was accepted" >&2
  exit 1
fi
for _ in {1..10}; do
  if [[ -z "$(docker exec "$relay" ss -Haun 'sport = :40124')" ]]; then break; fi
  sleep 1
done
run_relay remove "${args[@]}"
# Pause the startup after its first firewall write, then stop the container.
docker run -d --name "$relay_service" --network "container:$relay" \
  --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges \
  --read-only --tmpfs /run:exec --tmpfs /etc/awg-forge --entrypoint /usr/bin/dumb-init "$image" -- \
  bash -ec '
    mkdir /run/bin
    export REAL_IPTABLES="$(command -v iptables)"
    cat >/run/bin/iptables <<\EOF
#!/bin/bash
"$REAL_IPTABLES" "$@" || exit
if [[ "$*" == *"-A FORWARD"* && ! -e /run/startup-paused ]]; then
  touch /run/startup-paused
  sleep 20
fi
EOF
    chmod +x /run/bin/iptables
    export PATH=/run/bin:$PATH
    exec udp-relay.sh serve "$@"
  ' bash "${args[@]}" >/dev/null
for _ in {1..10}; do
  if docker exec "$relay_service" test -f /run/startup-paused; then break; fi
  sleep 1
done
if ! docker exec "$relay_service" test -f /run/startup-paused; then
  docker logs "$relay_service" >&2
  echo "startup did not reach the first firewall write" >&2
  exit 1
fi
docker stop -t 30 "$relay_service" >/dev/null
exit_code="$(docker inspect -f '{{.State.ExitCode}}' "$relay_service")"
rules="$(docker exec "$relay" iptables-save)"
if [[ "$exit_code" != 0 || "$rules" == *"awg-forge-relay-${args[0]}-"* ]]; then
  docker logs "$relay_service" >&2
  printf 'startup cleanup failed (exit %s)\n' "$exit_code" >&2
  exit 1
fi
docker rm "$relay_service" >/dev/null
printf 'OK SIGTERM during startup removes partial rules\n'

write_relay_compose() {
  local file="$1" relay_image="$2" destination="$3" port="$4" source="$5" interface="$6" runtime_mount
  # Exercise the SELinux Compose syntax on every Docker runner.
  runtime_mount="$(selinux_volume_suffix() { printf ':Z'; }; relay_runtime_mount)"
  cat >"$file" <<YAML
services:
  relay:
    image: "$relay_image"
    container_name: "$relay_service"
    network_mode: "container:$relay"
    read_only: true
    cap_drop: [ALL]
    cap_add: [NET_ADMIN]
    tmpfs: [/run, /etc/awg-forge]
    volumes:
$runtime_mount
    command: ["relay", "serve", "${args[0]}", "${args[1]}", "$destination", "$port", "$source", "$interface"]
YAML
}
current="$compose_dir/current.yaml"
next="$compose_dir/next.yaml"
relay_id="${args[0]}"
write_relay_compose "$current" "${RELAY_PREVIOUS_IMAGE:-$image}" "$target" 40123 auto auto
write_relay_compose "$next" "$image" "$target_ip" 40125 "$relay_ip" eth0
relay_dir="$compose_dir"
# Use a unique project because the fixture runs alongside other Compose tests.
relay_compose() {
  docker compose --project-name "$relay_service" --project-directory "$relay_dir" -f "$1" "${@:2}"
}

if [[ "${RELAY_PREVIOUS_IMAGE:-$image}" == "$image" ]]; then
  docker exec "$relay" iptables -A FORWARD -d "$target_ip" -p udp --dport 40123 -j DROP
fi
relay_compose "$current" up -d
if ! wait_for_rules "${args[@]}"; then
  docker logs --tail 30 "$relay_service" >&2
  exit 1
fi
docker inspect --format '{{json .HostConfig.Binds}}' "$relay_service" |
  jq -e 'any(.[]; split(":") | .[-2] == "/var/lib/awg-forge-relay" and (.[-1] | split(",") | index("Z") != null))' >/dev/null
printf 'OK Docker received the private SELinux label for the relay runtime bind mount\n'
if [[ "${RELAY_REQUIRE_SELINUX:-0}" == 1 ]]; then
  docker inspect --format '{{json .ProcessLabel}}' "$relay_service" | jq -e 'length > 0' >/dev/null
fi
if [[ "${RELAY_PREVIOUS_IMAGE:-$image}" != "$image" ]]; then
  # A legacy fixture may prepend its ACLs. Keep the deny test in front of them.
  docker exec "$relay" iptables -I FORWARD -d "$target_ip" -p udp --dport 40123 -j DROP
fi
docker exec "$relay" ip -4 addr add 192.0.2.1/32 dev lo
docker exec "$relay" ip -4 route add "$target_ip/32" dev eth0 src 192.0.2.1
if run_relay status "${args[@]}"; then
  echo "relay reported ready after its route source changed" >&2
  exit 1
fi
docker exec "$relay" ip -4 route del "$target_ip/32" dev eth0
docker exec "$relay" ip -4 addr del 192.0.2.1/32 dev lo
wait_for_rules "${args[@]}"
printf 'OK relay status follows the current route\n'
docker run -d --name "$no_forward" --network "$network" --cap-add NET_ADMIN \
  --sysctl net.ipv4.ip_forward=0 --entrypoint sleep "$image" infinity >/dev/null
docker exec "$relay" iptables-save | docker exec -i "$no_forward" iptables-restore
if (relay="$no_forward"; run_relay status "${args[@]}"); then
  echo "relay reported ready with IPv4 forwarding disabled" >&2
  exit 1
fi
docker rm -f "$no_forward" >/dev/null
docker exec "$client" perl -MIO::Socket::INET -MIO::Select -e '
  $socket = IO::Socket::INET->new(PeerAddr => $ARGV[0], PeerPort => 40124, Proto => "udp") or die $!;
  $socket->send("blocked") or die $!;
  die "relay bypassed FORWARD DROP" if IO::Select->new($socket)->can_read(2);
' "$relay_ip"
docker exec "$relay" iptables -nvxL FORWARD | awk '$3 == "DROP" && $1 > 0 { matched = 1 } END { exit !matched }'
docker exec "$relay" iptables -D FORWARD -d "$target_ip" -p udp --dport 40123 -j DROP
docker exec -d "$target" perl -MIO::Socket::INET -e '
  $socket = IO::Socket::INET->new(LocalPort => 40125, Proto => "udp") or die $!;
  while (1) {
    $peer = $socket->recv($payload, 2048) or die $!;
    $socket->send("new:$payload", 0, $peer) or die $!;
  }'
# Keep a connected UDP socket open across removal and reinstallation.
docker exec -d "$client" perl -MIO::Socket::INET -MIO::Select -e '
  alarm 35;
  $socket = IO::Socket::INET->new(PeerAddr => $ARGV[0], PeerPort => 40124, Proto => "udp") or die $!;
  for $expected ("old", "new") {
    if ($expected eq "new") {
      sleep 1 until -e "/tmp/relay-replacing";
      $socket->send("during-replacement") or die $!;
      open($file, ">", "/tmp/relay-in-flight") or die $!;
      print {$file} $socket->sockport or die $!;
      close($file) or die $!;
      sleep 1 until -e "/tmp/relay-replaced";
      # A datagram to the temporarily closed port can yield an ICMP error.
      if (IO::Select->new($socket)->can_read(0)) {
        $socket->recv($ignored, 2048);
      }
    }
    $socket->send("relay-smoke") or die $!;
    $socket->recv($payload, 2048) or die $!;
    die "unexpected reply: $payload" unless $payload eq "$expected:relay-smoke";
    open($file, ">", "/tmp/relay-$expected") or die $!;
    close($file) or die $!;
  }
' "$relay_ip"
wait_for_reply() {
  for _ in {1..10}; do
    if docker exec "$client" test -f "/tmp/relay-$1"; then return 0; fi
    sleep 1
  done
  echo "did not receive reply from $1 destination" >&2
  return 1
}
wait_for_reply old
if [[ "${RELAY_PREVIOUS_IMAGE:-$image}" == "$image" || "${RELAY_TEST_INTERRUPTED_REMOVE:-}" == 1 ]]; then
  relay_compose "$current" kill -s SIGKILL relay
  old_mappings=( -f ipv4 -p udp --dst-nat --orig-dst "$relay_ip" --dport 40124
    --reply-src "$target_ip" --reply-port-src 40123 )
  mappings="$(docker exec "$relay" conntrack -L "${old_mappings[@]}")"
  [[ -n "$mappings" ]]
  # Reproduce a firewall reload without touching conntrack. With a journal,
  # remove every rule carrying the relay ID, including the fallback tuples.
  docker exec "$relay" iptables -t nat -D PREROUTING -i eth0 -d "$relay_ip" -p udp --dport 40124 \
    -m comment --comment "awg-forge-relay-$relay_id-dnat" -j DNAT --to-destination "$target_ip:40123"
  if [[ "${RELAY_PREVIOUS_IMAGE:-$image}" == "$image" ]]; then
    docker exec "$relay" iptables -t nat -F POSTROUTING
    docker exec "$relay" iptables -F FORWARD
    docker exec "$relay" iptables -A FORWARD -p tcp --dport 20001 -j ACCEPT
  fi
  relay_remove_installation "$current"
  [[ ! -e "$compose_dir" ]]
  mappings="$(docker exec "$relay" conntrack -L "${old_mappings[@]}")"
  [[ -z "$mappings" ]]
  mkdir -m 0700 "$compose_dir"
  mkdir -m 0700 "$compose_dir/runtime"
  write_relay_compose "$next" "$image" "$target_ip" 40125 "$relay_ip" eth0
  printf 'OK interrupted removal clears conntrack after the relay rules disappear\n'
else
  relay_stop_for_replacement
fi
docker exec "$client" touch /tmp/relay-replacing
wait_for_reply in-flight
source_port="$(docker exec "$client" cat /tmp/relay-in-flight)"
[[ "$source_port" =~ ^[0-9]+$ ]]
docker exec "$relay" conntrack -L -f ipv4 -p udp --orig-dst "$relay_ip" --dport 40124 --sport "$source_port" \
  --reply-src "$relay_ip" --reply-port-src 40124 2>/dev/null | awk 'NF { found = 1 } END { exit !found }'
args[3]=40125
relay_compose "$next" up -d
wait_for_rules "${args[@]}"
docker exec "$client" touch /tmp/relay-replaced
wait_for_reply new
printf 'OK explicit FORWARD deny, connected UDP conflict and same-socket replacement with an in-flight datagram\n'
if run_relay serve fedcba9876543210 "${args[@]:1}"; then
  echo "duplicate relay port was accepted" >&2
  exit 1
fi
docker restart -t 40 "$relay_service" >/dev/null
wait_for_rules "${args[@]}"
docker stop -t 40 "$relay_service" >/dev/null
relay_runtime_action "$next" remove
if run_relay status "${args[@]}" 2>/dev/null; then
  echo "relay rules remain after removal" >&2
  exit 1
fi
docker exec "$relay" iptables -C FORWARD -p tcp --dport 20001 -j ACCEPT
printf 'OK UDP relay round trip, restart, port conflict and cleanup\n'

# The exit server checks its own AWG listener through a separate relay, without
# a regular client. Its HTTP port is bound only to local loopback for this test.
exit_server="forge-awg-exit-$suffix"
trap 'docker rm -fv "$exit_server" >/dev/null 2>&1 || :; cleanup' EXIT
docker run -d --name "$exit_server" --network "$network" --cap-add NET_ADMIN \
  --device /dev/net/tun --sysctl net.ipv4.ip_forward=1 -p 127.0.0.1::51821 \
  -e PASSWORD=public-ci-fixture -e WEBUI_HOST=0.0.0.0 -e WEBUI_PORT=51821 \
  -e DATABASE_MODE=off -e APPLY_CONFIG=true "$image" serve >/dev/null
web="http://$(docker port "$exit_server" 51821/tcp)"
cookie=""
for _ in {1..30}; do
  if headers="$(curl -fsS --max-time 2 -D - -o /dev/null -H "Origin: $web" \
    -H 'Content-Type: application/json' -d '{"password":"public-ci-fixture"}' "$web/api/login" 2>/dev/null)"; then
    cookie="$(awk 'tolower($1) == "set-cookie:" {split($2, cookie, ";"); print cookie[1]; exit}' <<< "$headers")"
    break
  fi
  sleep 1
done
if [[ -z "$cookie" ]]; then
  docker logs --tail 20 "$exit_server" >&2
  echo "could not log in to the exit server" >&2
  exit 1
fi
request() { curl -fsS --max-time 5 -H "Origin: $web" -H "Cookie: $cookie" "$@"; }
tunnel="$(request "$web/api/state" | jq -ce '.tunnels[0]')"
tunnel_id="$(jq -er '.id' <<< "$tunnel")"
tunnel_port="$(jq -er '.listen_port' <<< "$tunnel")"
tunnel_interface="$(jq -er '.interface' <<< "$tunnel")"
base="$web/api/tunnels/$tunnel_id/relays"
relay_id="$(request -H 'Content-Type: application/json' \
  -d "{\"name\":\"AWG smoke\",\"host\":\"$relay_ip\",\"port\":40124,\"target_host\":\"$exit_server\"}" \
  "$base" | jq -er '.relay.id')"
dns_args=("$relay_id" 40124 "$exit_server" "$tunnel_port" auto auto)

docker run -d --name "$relay_service" --network "container:$relay" \
  --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges \
  --read-only --tmpfs /run --tmpfs /etc/awg-forge "$image" relay serve "${dns_args[@]}" >/dev/null
for _ in {1..15}; do
  if docker exec "$relay_service" udp-relay.sh status "${dns_args[@]}" 2>/dev/null; then break; fi
  sleep 1
done
docker exec "$relay_service" udp-relay.sh status "${dns_args[@]}"

check_status() {
  local expected="$1"
  local result
  for _ in {1..85}; do
    result="$(request "$base/health" | jq -ce --arg id "$relay_id" '.health[$id]')"
    if jq -e --arg expected "$expected" '.status == $expected and .succeeded_at != null' <<< "$result" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  printf 'expected relay check %s, got %s\n' "$expected" "$result" >&2
  return 1
}
check_status reachable
for _ in {1..5}; do
  if [[ -z "$(docker exec "$exit_server" awg show "$tunnel_interface" peers)" ]]; then break; fi
  sleep 1
done
[[ -z "$(docker exec "$exit_server" awg show "$tunnel_interface" peers)" ]]
docker stop -t 40 "$relay_service" >/dev/null
check_status unreachable
printf 'OK DNS relay and periodic AWG handshake, peer cleanup and loss detection\n'

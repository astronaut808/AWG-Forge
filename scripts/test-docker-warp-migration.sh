#!/usr/bin/env bash
set -euo pipefail

image=${IMAGE:-awg-forge:local}
anchor="awg-warp-migration-${RANDOM}-${RANDOM}"
successor="$anchor-new"
data="$anchor-data"
sockets="$anchor-sockets"

cleanup() {
  docker rm -f "$successor" "$anchor" >/dev/null 2>&1 || true
  docker volume rm "$data" "$sockets" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Keep a disposable network namespace alive across different container filesystems.
# The socket volume is needed only when the fixture uses userspace on hosts lacking the module.
docker run -d --name "$anchor" --platform linux/amd64 --network none \
  --cap-add NET_ADMIN --device /dev/net/tun --sysctl net.ipv4.ip_forward=1 \
  -e APPLY_CONFIG=false -e DATABASE_MODE=off \
  -v "$data:/etc/awg-forge" -v "$sockets:/var/run/amneziawg" \
  --entrypoint /bin/sleep "$image" infinity >/dev/null

docker exec "$anchor" ip link add testwan0 type dummy
docker exec "$anchor" ip link set testwan0 up
docker exec "$anchor" awg-forge init --server-host 127.0.0.1 --external-interface testwan0 \
  --profile awg_legacy_1_0 --tunnel-name awg0 --listen-port 51820 --ipv4-subnet 10.8.0.0/24 >/dev/null
docker exec "$anchor" awg-forge tunnel create awg_legacy_1_0 awg1 51821 10.9.0.0/24 >/dev/null
docker exec -e APPLY_CONFIG=true -e EXTERNAL_INTERFACE=testwan0 "$anchor" awg-forge render >/dev/null

docker exec "$anchor" sh -ec '
  umask 077
  awg genkey >/tmp/warp-test-private
  awg genkey | awg pubkey >/tmp/warp-test-peer
  cat /tmp/warp-test-private /tmp/warp-test-peer /etc/awg-forge/state.json
' | node --input-type=module -e '
  import { readFileSync } from "node:fs";
  const [privateKey, peerPublicKey, ...json] = readFileSync(0, "utf8").split("\n");
  const state = JSON.parse(json.join("\n"));
  state.warp = {
    interface_name: "warp0", private_key: privateKey + " # imported inline comment", peer_public_key: peerPublicKey,
    endpoint: "127.0.0.1:2408", address_v4: "172.16.0.2", mtu: 1280,
  };
  state.tunnels[0].egress_mode = "warp";
  state.tunnels[1].egress_mode = "wan";
  process.stdout.write(JSON.stringify(state));
' | docker exec -i "$anchor" sh -ec '
  umask 077
  cat >/etc/awg-forge/.warp-test-state
  mv /etc/awg-forge/.warp-test-state /etc/awg-forge/state.json
'

backend=$(docker exec "$anchor" sh -ec '
  umask 077
  mkdir -p /etc/amnezia/amneziawg
  printf "%s\n" "[Interface]" "PrivateKey = $(cat /tmp/warp-test-private)" \
    "Address = 172.16.0.2/32" "MTU = 1280" "Table = 200" \
    "PostUp = ip route replace 10.8.0.0/24 dev awg0 table 200; ip rule add from 10.8.0.0/24 lookup 200" \
    "PostUp = ip route replace 10.9.0.0/24 dev awg1 table 200; ip rule add from 10.9.0.0/24 lookup 200" \
    "[Peer]" "PublicKey = $(cat /tmp/warp-test-peer)" \
    "AllowedIPs = 0.0.0.0/0" "Endpoint = 127.0.0.1:2408" >/etc/amnezia/amneziawg/warp0.conf
  rm /tmp/warp-test-private /tmp/warp-test-peer
  if ip link add awgprobe0 type amneziawg 2>/dev/null; then
    ip link del awgprobe0
    AWG_QUICK_FORCE_USERSPACE=0 awg-quick up warp0 >&2
    test ! -S /var/run/amneziawg/warp0.sock
    echo kernel
  else
    AWG_QUICK_FORCE_USERSPACE=1 awg-quick up warp0 >&2
    test -S /var/run/amneziawg/warp0.sock
    echo userspace-fixture
  fi
')
old_index=$(docker exec "$anchor" cat /sys/class/net/warp0/ifindex)
docker exec "$anchor" ip -4 rule add from 10.253.0.0/24 lookup 200
docker exec "$anchor" sh -ec '
  ip -4 rule show from 10.9.0.0/24 table 200 | grep -q "lookup 200"
  ip -4 route show table 200 exact 10.9.0.0/24 | grep -q "^10\.9\.0\.0/24 dev awg1 "
'

# The replacement sees the same legacy link but has a fresh /etc/amnezia.
docker run --rm --platform linux/amd64 --network "container:$anchor" --entrypoint /bin/sh \
  "$image" -ec 'ip link show warp0 >/dev/null; test ! -e /etc/amnezia/amneziawg/warp0.conf'
docker run -d --name "$successor" --platform linux/amd64 --network "container:$anchor" \
  --cap-add NET_ADMIN --device /dev/net/tun -e DATABASE_MODE=off -e EXTERNAL_INTERFACE=testwan0 \
  -v "$data:/etc/awg-forge" -v "$sockets:/var/run/amneziawg" "$image" >/dev/null

for _ in $(seq 1 30); do
  if docker exec "$successor" sh -ec '
    test -S /var/run/amneziawg/warp0.sock
    test "$(cat /sys/class/net/warp0/ifindex)" != "$1"
    ip -4 route show table 200 | grep -q "^default dev warp0 "
    test "$(ip -4 rule show from 10.8.0.0/24 table 200 | wc -l)" -eq 1
    wan_rule=$(ip -4 rule show from 10.9.0.0/24 table 200)
    wan_route=$(ip -4 route show table 200 exact 10.9.0.0/24)
    test -z "$wan_rule$wan_route"
    test "$(ip -4 rule show from 10.253.0.0/24 table 200 | wc -l)" -eq 1
    test "$(stat -c %a /etc/amnezia/amneziawg/warp0.conf)" = 600
  ' sh "$old_index" 2>/dev/null; then
    echo "OK WARP migration ($backend to userspace): commented key accepted, stale WAN policy removed"
    exit 0
  fi
  sleep 1
done

echo "WARP migration failed from $backend: replacement interface or routing did not become ready" >&2
exit 1

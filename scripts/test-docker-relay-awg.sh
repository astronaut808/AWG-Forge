#!/usr/bin/env bash
set -Eeuo pipefail
trap 'printf "relay AWG smoke failed at line %s (exit %s)\n" "$LINENO" "$?" >&2' ERR

image="${IMAGE:-awg-forge:local}"
prefix="forge-relay-awg-$$-$RANDOM"
network="$prefix-net"
exit_node="$prefix-exit"
relay_node="$prefix-router"
client="$prefix-client"
relay_primary="$prefix-primary"
relay_secondary="$prefix-secondary"
cleanup() {
  docker rm -fv "$relay_primary" "$relay_secondary" "$client" "$relay_node" "$exit_node" >/dev/null 2>&1 || :
  docker network rm "$network" >/dev/null 2>&1 || :
}
trap cleanup EXIT

docker network create "$network" >/dev/null
docker run -d --name "$relay_node" --network "$network" --cap-add NET_ADMIN \
  --sysctl net.ipv4.ip_forward=1 --entrypoint sleep "$image" infinity >/dev/null
docker run -d --name "$client" --network "$network" --cap-add NET_ADMIN \
  --device /dev/net/tun --entrypoint sleep "$image" infinity >/dev/null
docker run -d --name "$exit_node" --network "$network" --cap-add NET_ADMIN \
  --device /dev/net/tun --entrypoint dumb-init "$image" -- bash -ec '
    until [[ -f /etc/awg-forge/ready ]]; do sleep 0.2; done
    ip -4 addr add 192.0.2.66/32 dev eth0
    ip rule add priority 19200 to 192.0.2.200/32 lookup 19200
    ip route add blackhole 192.0.2.200/32 table 19200
    iptables -A INPUT -p tcp --dport 20001 -j ACCEPT
    iptables -t mangle -A INPUT -p udp --dport 40130 -d 192.0.2.66
    iptables -t mangle -A OUTPUT -p udp --sport 40130 -s 192.0.2.66 -j ACCEPT
    amneziawg-go exitwg >/dev/null
    awg setconf exitwg /etc/awg-forge/exit.conf
    ip -4 addr add 10.203.64.1/24 dev exitwg
    ip link set exitwg up
    exec perl -MIO::Socket::INET -e '\''
      $socket = IO::Socket::INET->new(LocalAddr => "10.203.64.1", LocalPort => 40140, Proto => "udp") or die $!;
      while (1) {
        $peer = $socket->recv($payload, 2048) or die $!;
        $socket->send("exit:$payload", 0, $peer) or die $!;
      }
    '\''
  ' >/dev/null

container_ip() { docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"; }
primary_ip="$(container_ip "$exit_node")"
relay_ip="$(container_ip "$relay_node")"
secondary_ip=192.0.2.66
docker exec "$relay_node" ip -4 route add "$secondary_ip/32" via "$primary_ip"
docker exec "$relay_node" iptables -P FORWARD DROP
docker exec "$relay_node" iptables -A FORWARD -p tcp --dport 20001 -j ACCEPT

awg_parameters() { printf 'H1 = 21\nH2 = 22\nH3 = 23\nH4 = 24\n'; }
private="$(docker exec "$exit_node" awg genkey)"
server_public="$(docker exec -i "$exit_node" awg pubkey <<< "$private")"
{
  printf '[Interface]\nPrivateKey = %s\nListenPort = 40130\n' "$private"
  awg_parameters
  for lane in 0 1 2; do
    case "$lane" in
      0) endpoint="$relay_ip:40131" ;;
      1) endpoint="$relay_ip:40132" ;;
      2) endpoint="$primary_ip:40130" ;;
    esac
    client_private="$(docker exec "$client" awg genkey)"
    client_public="$(docker exec -i "$client" awg pubkey <<< "$client_private")"
    printf '\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.203.64.%s/32\n' "$client_public" "$((lane + 2))"
    {
      printf '[Interface]\nPrivateKey = %s\nListenPort = %s\n' "$client_private" "$((40150 + lane))"
      awg_parameters
      printf '\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 10.203.64.1/32\n' "$server_public" "$endpoint"
    } | docker exec -i "$client" sh -ec 'umask 077; cat > "/tmp/lane$1.conf"' sh "$lane"
  done
} | docker exec -i "$exit_node" sh -ec 'umask 077; cat > /etc/awg-forge/exit.conf; touch /etc/awg-forge/ready'

primary_args=(1234567890abcdef 40131 "$primary_ip" 40130 "$relay_ip" eth0)
secondary_args=(fedcba0987654321 40132 "$secondary_ip" 40130 "$relay_ip" eth0)
start_relay() {
  local name="$1"
  shift
  docker run -d --name "$name" --network "container:$relay_node" \
    --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges \
    --read-only --tmpfs /run --tmpfs /etc/awg-forge "$image" relay serve "$@" >/dev/null
}
ready_relay() {
  local name="$1"
  shift
  for _ in {1..20}; do
    if docker exec "$name" udp-relay.sh status "$@" 2>/dev/null; then return 0; fi
    sleep 0.5
  done
  docker logs --tail 12 "$name" >&2
  docker exec "$name" udp-relay.sh status "$@"
}
start_relay "$relay_primary" "${primary_args[@]}"
start_relay "$relay_secondary" "${secondary_args[@]}"
ready_relay "$relay_primary" "${primary_args[@]}"
ready_relay "$relay_secondary" "${secondary_args[@]}"

prove_lane() {
  # Separate routing tables send each source IP over its own tunnel. The
  # three connected UDP sockets use distinct endpoints at the same time.
  docker exec "$client" bash -ec '
    lane="$1"
    address="10.203.64.$((lane + 2))"
    table="$((19300 + lane))"
    name="lane$lane"
    amneziawg-go "$name" >/dev/null
    awg setconf "$name" "/tmp/$name.conf"
    ip -4 addr add "$address/32" dev "$name"
    ip link set "$name" up
    ip route add 10.203.64.1/32 dev "$name" table "$table"
    ip rule add priority "$table" from "$address/32" lookup "$table"
    trap '\''ip rule del priority "$table"; ip link del "$name"'\'' EXIT
    perl -MIO::Socket::INET -MIO::Select -e '\''
      $socket = IO::Socket::INET->new(LocalAddr => $ARGV[0], PeerAddr => "10.203.64.1", PeerPort => 40140, Proto => "udp") or die $!;
      $poll = IO::Select->new($socket);
      for (1 .. 12) {
        $socket->send($ARGV[1]) or die $!;
        last if $poll->can_read(1) && defined $socket->recv($reply, 2048) && $reply eq "exit:$ARGV[1]";
      }
      die "no authenticated round trip on $ARGV[1]" unless $reply eq "exit:$ARGV[1]";
    '\'' "$address" "$name-$2"
    awg show "$name" latest-handshakes | awk '\''NR == 1 && $2 > 0 { found = 1 } END { exit !found }'\''
  ' bash "$@" && printf 'OK AWG handshake and round trip for %s lane %s\n' "$2" "$1"
}
secondary_replies() {
  docker exec "$exit_node" iptables -t mangle -nvxL OUTPUT | awk -v source="$secondary_ip" '
    $3 == "ACCEPT" && $8 == source && $0 ~ /spt:40130$/ { print $1; found = 1 } END { exit !found }
  '
}

prove_traffic() {
  local phase="$1" lane pid before after failed=0
  local -a pids=()
  for _ in {1..30}; do
    if [[ -n "$(docker exec "$exit_node" ss -Hlun 'sport = :40140')" ]]; then break; fi
    sleep 0.5
  done
  # Linux routing on the exit prefers the primary address, even for the
  # simultaneous relayed AWG peer that connects to the secondary address.
  docker exec "$exit_node" ip -4 route get "$relay_ip" | grep -F "src $primary_ip " >/dev/null
  before="$(secondary_replies)"
  for lane in 0 1 2; do
    prove_lane "$lane" "$phase" &
    pids+=("$!")
  done
  for pid in "${pids[@]}"; do
    wait "$pid" || failed=1
  done
  if (( failed )); then
    docker exec "$exit_node" iptables -t mangle -nvxL
    docker exec "$relay_node" iptables -nvxL FORWARD
    docker exec "$relay_node" conntrack -L -f ipv4 -p udp --orig-dst "$relay_ip" | awk '/sport=40130|dport=4013[12]/'
    return 1
  fi
  after="$(secondary_replies)"
  (( after > before ))
  docker exec "$exit_node" awg show exitwg latest-handshakes | awk '$2 > 0 { count++ } END { exit count != 3 }'
}

prove_traffic initial
docker restart -t 5 "$exit_node" >/dev/null
prove_traffic exit-restart
docker restart -t 40 "$relay_primary" "$relay_secondary" >/dev/null
ready_relay "$relay_primary" "${primary_args[@]}"
ready_relay "$relay_secondary" "${secondary_args[@]}"
prove_traffic relay-restart
docker exec "$exit_node" ip -4 addr del "$secondary_ip/32" dev eth0
docker exec "$exit_node" ip -4 addr add "$secondary_ip/32" dev eth0
prove_traffic address-reconfigured
docker exec "$relay_node" iptables -C FORWARD -p tcp --dport 20001 -j ACCEPT
docker exec "$exit_node" iptables -C INPUT -p tcp --dport 20001 -j ACCEPT
docker exec "$exit_node" ip rule show | grep -E '^19200:.*lookup 19200$' >/dev/null
docker exec "$exit_node" ip -4 route show table 19200 exact 192.0.2.200/32 |
  awk '$1 == "blackhole" { found = 1 } END { exit !found }'

# A wildcard UDP listener without IP_PKTINFO replies from the primary IP.
# This deliberately reproduces the mismatched tuple even with the fixed AWG.
docker stop -t 40 "$relay_secondary" >/dev/null
docker rm -v "$relay_secondary" >/dev/null
secondary_args[3]=40135
start_relay "$relay_secondary" "${secondary_args[@]}"
ready_relay "$relay_secondary" "${secondary_args[@]}"
diagnose_secondary() {
  docker exec "$relay_secondary" udp-relay.sh diagnose "${secondary_args[@]}"
}
no_mismatch='relay: no matching evidence of a reply source mismatch in current conntrack entries'
mismatch="relay: possible reply source mismatch (expected $secondary_ip, observed $primary_ip)"
diagnose_secondary | grep -Fx "$no_mismatch"

# Inject only fixture tuples into this test namespace. The translated
# source port differs from the client port, and the expected reply zone
# differs from the request's original zone.
docker exec "$relay_node" conntrack -I -p udp --orig-src "$(container_ip "$client")" \
  --orig-dst "$relay_ip" --sport 40160 --dport 40132 --reply-zone 7 --timeout 60 \
  --dst-nat "$secondary_ip:40135" --src-nat "$relay_ip:40161"
wrong_flow=(-p udp --orig-src "$primary_ip" --orig-dst "$relay_ip" --sport 40135 --dport 40161)
docker exec "$relay_node" conntrack -I "${wrong_flow[@]}" --zone 6 --timeout 60
diagnose_secondary | grep -Fx "$no_mismatch"
docker exec "$relay_node" conntrack -I "${wrong_flow[@]}" --zone 7 --timeout 60 --status ASSURED,SEEN_REPLY
diagnose_secondary | grep -Fx "$no_mismatch"
docker exec "$relay_node" conntrack -D "${wrong_flow[@]}" --zone 7
docker exec "$relay_node" conntrack -I "${wrong_flow[@]}" --orig-zone 7 --timeout 60
diagnose_secondary | grep -Fx "$mismatch"
docker exec "$relay_node" conntrack -D "${wrong_flow[@]}" --orig-zone 7
diagnose_secondary | grep -Fx "$no_mismatch"

docker exec -d "$exit_node" perl -MIO::Socket::INET -e '
  $socket = IO::Socket::INET->new(LocalPort => 40135, Proto => "udp") or die $!;
  alarm 15;
  $peer = $socket->recv($payload, 2048) or die $!;
  $socket->send("test", 0, $peer) or die $!;
'
for _ in {1..10}; do
  if [[ -n "$(docker exec "$exit_node" ss -Hlun 'sport = :40135')" ]]; then break; fi
  sleep 0.5
done
docker exec "$client" perl -MIO::Socket::INET -MIO::Select -e '
  $socket = IO::Socket::INET->new(LocalPort => 40162, PeerAddr => $ARGV[0], PeerPort => 40132, Proto => "udp") or die $!;
  $socket->send("test") or die $!;
  die "unexpected reverse NAT" if IO::Select->new($socket)->can_read(1);
' "$relay_ip"
diagnose_secondary | grep -Fx "$mismatch"
printf 'OK docker relay AWG multi-IP traffic and restart\n'

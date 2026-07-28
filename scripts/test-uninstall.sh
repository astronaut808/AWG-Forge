#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$repo_root/uninstall.sh"

assert_equal() {
  local got="$1"
  local want="$2"
  local label="$3"
  if [[ "$got" != "$want" ]]; then
    printf 'FAIL %s\nwant: %q\ngot:  %q\n' "$label" "$want" "$got" >&2
    exit 1
  fi
}

assert_contains() {
  local got="$1"
  local want="$2"
  local label="$3"
  if [[ "$got" != *"$want"* ]]; then
    printf 'FAIL %s\nmissing: %q\ngot:     %q\n' "$label" "$want" "$got" >&2
    exit 1
  fi
}

test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT
cd "$test_dir"

mkdir -p data custom
cat > data/state.json <<'EOF'
{
  "external_interface": "ens3",
  "warp": {
    "interface_name": "warp0",
    "private_key": "test-private-key"
  },
  "tunnels": [
    {
      "id": "0123456789abcdef",
      "interface_name": "awg0",
      "enabled": true,
      "listen_port": 51820,
      "ipv4_subnet": "10.8.0.0/24",
      "egress_mode": "warp",
      "clients": [
        {
          "id": "client-1",
          "enabled": true
        }
      ]
    },
    {
      "id": "fedcba9876543210",
      "interface_name": "awg20",
      "enabled": true,
      "listen_port": 51821,
      "ipv4_subnet": "10.20.0.0/24"
    }
  ]
}
EOF
printf 'CONFIG_DIR=%s\n' "$test_dir/custom" > .env

assert_equal "$(state_path)" "data/state.json" "state path"
assert_equal "$(state_external_interface "$(state_path)")" "ens3" "external interface"
assert_equal "$(state_tunnels "$(state_path)")" $'0123456789abcdef|awg0|51820|10.8.0.0/24|warp|true\nfedcba9876543210|awg20|51821|10.20.0.0/24||true' "tunnel state"
assert_equal "$(state_warp_interface "$(state_path)")" "warp0" "WARP interface"

printf 'OK   uninstall reads managed tunnels and external interface from host state\n'

DRY_RUN=true
have() {
  [[ "$1" == "iptables" ]]
}
iptables() {
  return 0
}

output="$(iptables_delete_all "" FORWARD -i awg0 -j ACCEPT)"
assert_equal "$output" "DRY iptables -D FORWARD -i awg0 -j ACCEPT" "legacy forward cleanup"

printf 'OK   uninstall dry-run terminates when an iptables rule exists\n'

output="$(cleanup_tunnel_rules "0123456789abcdef" "awg0" "51820" "10.8.0.0/24" "ens3")"
assert_contains "$output" "-t nat -D POSTROUTING -s 10.8.0.0/24 -o ens3 -j MASQUERADE" "WAN legacy NAT cleanup"
assert_contains "$output" "-t nat -D POSTROUTING -s 10.8.0.0/24 -o warp0 -j MASQUERADE" "WARP legacy NAT cleanup"
assert_contains "$output" "--comment awg-forge-0123456789abcdef-masquerade" "tagged NAT cleanup"
assert_contains "$output" "--comment awg-forge-0123456789abcdef-input-udp" "tagged input cleanup"
assert_contains "$output" "--comment awg-forge-0123456789abcdef-forward-egress" "tagged egress cleanup"
assert_contains "$output" "--comment awg-forge-0123456789abcdef-forward-return" "tagged return cleanup"

printf 'OK   uninstall dry-run removes legacy and tagged managed firewall rules\n'

warp_output="$(cleanup_tunnel_rules "0123456789abcdef" "awg0" "51820" "10.8.0.0/24" "warp0")"
assert_equal "$(grep -Fxc 'DRY iptables -t nat -D POSTROUTING -s 10.8.0.0/24 -o warp0 -j MASQUERADE' <<< "$warp_output")" "1" "single WARP legacy NAT cleanup"

printf 'OK   uninstall does not duplicate legacy WARP cleanup when WARP is the external interface\n'

have() {
  [[ "$1" == "iptables" || "$1" == "ip" ]]
}
output="$(cleanup_warp_route "awg0" "10.8.0.0/24")"
assert_contains "$output" "DRY ip rule del from 10.8.0.0/24 lookup 200" "WARP policy rule cleanup"
assert_contains "$output" "DRY ip route del 10.8.0.0/24 dev awg0 table 200" "WARP route cleanup"

printf 'OK   uninstall dry-run removes WARP policy routing\n'

output="$(run_compose "docker compose" down --remove-orphans)"
assert_equal "$output" "DRY docker compose down --remove-orphans" "Compose dry run"

printf 'OK   uninstall dry-run does not execute docker compose\n'

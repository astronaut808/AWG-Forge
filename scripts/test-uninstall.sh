#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$repo_root/uninstall.sh"

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
      "egress_mode": "warp"
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

[[ "$(state_path)" == "data/state.json" ]]
[[ "$(state_external_interface "$(state_path)")" == "ens3" ]]
[[ "$(state_tunnels "$(state_path)")" == $'0123456789abcdef|awg0|51820|10.8.0.0/24|warp|true\nfedcba9876543210|awg20|51821|10.20.0.0/24||true' ]]
[[ "$(state_warp_interface "$(state_path)")" == "warp0" ]]

printf 'OK   uninstall reads managed tunnels and external interface from host state\n'

DRY_RUN=true
have() {
  [[ "$1" == "iptables" ]]
}
iptables() {
  return 0
}

output="$(iptables_delete_all "" FORWARD -i awg0 -j ACCEPT)"
[[ "$output" == "DRY iptables -D FORWARD -i awg0 -j ACCEPT" ]]

printf 'OK   uninstall dry-run terminates when an iptables rule exists\n'

output="$(cleanup_tunnel_rules "0123456789abcdef" "awg0" "51820" "10.8.0.0/24" "ens3")"
[[ "$output" == *"--comment awg-forge-0123456789abcdef-masquerade"* ]]
[[ "$output" == *"--comment awg-forge-0123456789abcdef-input-udp"* ]]
[[ "$output" == *"--comment awg-forge-0123456789abcdef-forward-egress"* ]]
[[ "$output" == *"--comment awg-forge-0123456789abcdef-forward-return"* ]]

printf 'OK   uninstall dry-run removes tagged managed firewall rules\n'

have() {
  [[ "$1" == "iptables" || "$1" == "ip" ]]
}
output="$(cleanup_warp_route "awg0" "10.8.0.0/24")"
[[ "$output" == *"DRY ip rule del from 10.8.0.0/24 lookup 200"* ]]
[[ "$output" == *"DRY ip route del 10.8.0.0/24 dev awg0 table 200"* ]]

printf 'OK   uninstall dry-run removes WARP policy routing\n'

output="$(run_compose "docker compose" down --remove-orphans)"
[[ "$output" == "DRY docker compose down --remove-orphans" ]]

printf 'OK   uninstall dry-run does not execute docker compose\n'

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
  "tunnels": [
    {
      "interface_name": "awg0",
      "enabled": true,
      "listen_port": 51820,
      "ipv4_subnet": "10.8.0.0/24"
    },
    {
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
[[ "$(state_tunnels "$(state_path)")" == $'awg0|51820|10.8.0.0/24|true\nawg20|51821|10.20.0.0/24|true' ]]

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

output="$(run_compose "docker compose" down --remove-orphans)"
[[ "$output" == "DRY docker compose down --remove-orphans" ]]

printf 'OK   uninstall dry-run does not execute docker compose\n'

#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$repo_root/install.sh"

test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT
cd "$test_dir"

if existing_install_found; then
  printf 'FAIL clean directory was detected as an existing install\n' >&2
  exit 1
fi

handle_existing_install ""

printf 'OK   clean install continues past existing-install detection\n'

touch "$ENV_FILE"
if ! existing_install_found; then
  printf 'FAIL existing install was not detected\n' >&2
  exit 1
fi

prompt() {
  printf '1'
}

handle_existing_install ""

printf 'OK   existing install still reaches the action selection\n'

rm -f "$ENV_FILE"
mkdir -p "$DATA_DIR"
write_env "127.0.0.1" "51821" "password" "secret" "eth0"
if grep -Eq '^(SERVER_HOST|TUNNEL_NAME|LISTEN_PORT|IPV4_SUBNET|DNS|ALLOWED_IPS|PERSISTENT_KEEPALIVE|MTU|PROTOCOL_PROFILE)=' "$ENV_FILE"; then
  printf 'FAIL runtime .env contains tunnel bootstrap variables\n' >&2
  exit 1
fi
write_bootstrap "vpn.example.com" "awg20" "51830" "eth0" "10.20.0.0/24" "1.1.1.1" "0.0.0.0/0" "0" "0" "awg_2_0"
if ! grep -q '^PROTOCOL_PROFILE=awg_2_0$' "$DATA_DIR/bootstrap.env"; then
  printf 'FAIL bootstrap.env did not contain the selected tunnel profile\n' >&2
  exit 1
fi

printf 'OK   runtime env and one-time bootstrap are split\n'

missing_docker_dir="$test_dir/must-not-exist"
INSTALL_DIR_DEFAULT="$missing_docker_dir"
unset AWG_FORGE_HOME
uname() {
  printf 'Linux'
}
have() {
  [[ "$1" != "docker" ]]
}

if (main >"$test_dir/no-docker.log" 2>&1); then
  printf 'FAIL installer succeeded without Docker\n' >&2
  exit 1
fi
if [[ -e "$missing_docker_dir" ]]; then
  printf 'FAIL installer created files before checking Docker\n' >&2
  exit 1
fi
if ! grep -q 'https://docs.docker.com/engine/install/' "$test_dir/no-docker.log"; then
  printf 'FAIL installer did not print Docker installation documentation\n' >&2
  exit 1
fi

printf 'OK   missing Docker exits before creating the install directory\n'

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

prompt() {
  printf '3'
}
INSTALL_ACTION=fresh
handle_existing_install ""
if [[ "$INSTALL_ACTION" != "upgrade" ]]; then
  printf 'FAIL existing install did not select upgrade\n' >&2
  exit 1
fi

printf 'OK   existing install offers the safe upgrade path\n'

rm -f "$ENV_FILE"
mkdir -p "$DATA_DIR"
write_env "127.0.0.1" "51821" "password" "secret" "eth0"
if grep -Eq '^(SERVER_HOST|TUNNEL_NAME|LISTEN_PORT|IPV4_SUBNET|DNS|ALLOWED_IPS|PERSISTENT_KEEPALIVE|MTU|PROTOCOL_PROFILE)=' "$ENV_FILE"; then
  printf 'FAIL runtime .env contains tunnel init variables\n' >&2
  exit 1
fi
if [[ -e "$DATA_DIR/bootstrap.env" ]]; then
  printf 'FAIL installer created deprecated bootstrap.env\n' >&2
  exit 1
fi

printf 'OK   runtime env is split from explicit state init\n'

if ! grep -qx 'DATABASE_MODE=sqlite' "$ENV_FILE"; then
  printf 'FAIL fresh install does not enable SQLite by default\n' >&2
  exit 1
fi

printf 'OK   fresh install enables SQLite by default\n'

cat >"$ENV_FILE" <<'EOF'
WEBUI_HOST=127.0.0.1
WEBUI_PORT=51821
PASSWORD=existing-password
SESSION_SECRET=existing-secret
EXTERNAL_INTERFACE=eth0
DATABASE_MODE=sqlite
WEBUI_TLS_MODE=manual
WEBUI_TLS_CERT_FILE=/etc/awg-forge/tls/cert.pem
EOF
write_env "127.0.0.1" "51900" "existing-password" "existing-secret" "ens3" reconfigure
if ! grep -qx 'DATABASE_MODE=sqlite' "$ENV_FILE" || ! grep -qx 'WEBUI_TLS_MODE=manual' "$ENV_FILE" || ! grep -qx 'WEBUI_TLS_CERT_FILE=/etc/awg-forge/tls/cert.pem' "$ENV_FILE"; then
  printf 'FAIL reconfigure did not preserve existing operational settings\n' >&2
  exit 1
fi
if ! grep -qx 'WEBUI_PORT=51900' "$ENV_FILE" || ! grep -qx 'EXTERNAL_INTERFACE=ens3' "$ENV_FILE"; then
  printf 'FAIL reconfigure did not update selected runtime settings\n' >&2
  exit 1
fi
if ! grep -qx 'PASSWORD=existing-password' "$ENV_FILE" || ! grep -qx 'SESSION_SECRET=existing-secret' "$ENV_FILE"; then
  printf 'FAIL reconfigure changed existing credentials\n' >&2
  exit 1
fi

printf 'OK   reconfigure preserves operational settings\n'

if grep -Eq '^[[:space:]]*(TUNNEL_NAME|LISTEN_PORT|IPV4_SUBNET|DNS|ALLOWED_IPS|PERSISTENT_KEEPALIVE|MTU|PROTOCOL_PROFILE)=' "$repo_root/Dockerfile"; then
  printf 'FAIL Docker image ENV contains tunnel init variables\n' >&2
  exit 1
fi

printf 'OK   Docker image env does not contain tunnel init variables\n'

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

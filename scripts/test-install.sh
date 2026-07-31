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

if ! grep -qx 'TUNNEL_UDP_PORT_RANGE=30000-49999' "$ENV_FILE"; then
  printf 'FAIL fresh install does not configure the automatic UDP port range\n' >&2
  exit 1
fi

if grep -q '^WEBUI_TLS_' "$ENV_FILE"; then
	printf 'FAIL fresh install stores TLS desired configuration in .env\n' >&2
	exit 1
fi

docker() {
  printf '%s\n' "$*" >"$test_dir/docker-command"
}
configure_tls "docker compose" off "" ""
if [[ "$(<"$test_dir/docker-command")" != "compose run --rm --no-deps awg-forge tls disable" ]]; then
  printf 'FAIL TLS bootstrap did not invoke docker compose as separate arguments\n' >&2
  exit 1
fi
unset -f docker

printf 'OK   fresh install configures the automatic UDP port range\n'

valid_acme_domain="$(normalize_acme_domain ' Panel.Example.com. ')"
if [[ "$valid_acme_domain" != "panel.example.com" ]]; then
  printf 'FAIL installer did not normalize a valid ACME DNS name\n' >&2
  exit 1
fi
for invalid_acme_domain in 'foo..example.com' 'foo-.example.com' '-foo.example.com' '203.0.113.4' '*.example.com'; do
  if normalize_acme_domain "$invalid_acme_domain" >/dev/null; then
    printf 'FAIL installer accepted invalid ACME DNS name: %s\n' "$invalid_acme_domain" >&2
    exit 1
  fi
done

printf 'OK   ACME DNS validation matches the managed TLS constraints\n'

mkdir -p "$DATA_DIR/tls"
printf '{"mode":"acme-domain"}\n' >"$DATA_DIR/tls/config.json"
if ! managed_acme_tls_configured; then
  printf 'FAIL installer did not detect managed ACME TLS\n' >&2
  exit 1
fi
if ! is_loopback_webui_host 127.0.0.1 || ! is_loopback_webui_host localhost || is_loopback_webui_host 0.0.0.0; then
  printf 'FAIL installer did not classify Web UI bind addresses correctly\n' >&2
  exit 1
fi
rm -rf "$DATA_DIR/tls"

printf 'OK   installer detects managed ACME before restricting Web UI access\n'

random_u32() {
  printf '1'
}
port_in_use_udp() {
  [[ "$1" == "30001" ]]
}
if [[ "$(random_available_udp_port "30000-30002")" != "30002" ]]; then
  printf 'FAIL automatic UDP port selection did not skip an occupied port\n' >&2
  exit 1
fi

printf 'OK   automatic UDP port selection skips occupied ports\n'

write_compose_if_missing
if ! grep -A4 '^    logging:$' "$COMPOSE_FILE" | grep -qx '      driver: local'; then
  printf 'FAIL fresh managed Compose does not use bounded local logging\n' >&2
  exit 1
fi
if ! grep -A4 '^    logging:$' "$COMPOSE_FILE" | grep -qx '        max-size: "10m"'; then
  printf 'FAIL fresh managed Compose does not set a log size limit\n' >&2
  exit 1
fi

printf 'OK   fresh managed Compose uses bounded runtime logging\n'

cat >"$ENV_FILE" <<'EOF'
WEBUI_HOST=127.0.0.1
WEBUI_PORT=51821
PASSWORD=existing-password
SESSION_SECRET=existing-secret
EXTERNAL_INTERFACE=eth0
DATABASE_MODE=sqlite
EOF
write_env "127.0.0.1" "51900" "existing-password" "existing-secret" "ens3" reconfigure
if ! grep -qx 'DATABASE_MODE=sqlite' "$ENV_FILE"; then
	printf 'FAIL reconfigure did not preserve existing operational settings\n' >&2
	exit 1
fi
if grep -q '^WEBUI_TLS_' "$ENV_FILE"; then
	printf 'FAIL reconfigure preserves obsolete TLS environment settings\n' >&2
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

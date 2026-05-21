#!/usr/bin/env bash
set -euo pipefail

APP_NAME="awg-forge"
INSTALL_DIR_DEFAULT="/opt/awg-forge"
ENV_FILE=".env"
COMPOSE_FILE="docker-compose.yml"
DATA_DIR="data"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
muted() { printf '\033[2m%s\033[0m\n' "$*"; }
ok() { printf '\033[32mOK\033[0m   %s\n' "$*"; }
warn() { printf '\033[33mWARN\033[0m %s\n' "$*"; }
fail() { printf '\033[31mERR\033[0m  %s\n' "$*" >&2; }

require_tty() {
  if [[ ! -r /dev/tty ]]; then
    fail "interactive install requires a TTY"
    printf 'Run this command from an interactive shell, not from a non-interactive job.\n' >&2
    exit 1
  fi
}

prompt() {
  local label="$1"
  local default="${2:-}"
  local value
  if [[ -n "$default" ]]; then
    printf '%s [%s]: ' "$label" "$default" > /dev/tty
    read -r value < /dev/tty
    printf '%s' "${value:-$default}"
  else
    printf '%s: ' "$label" > /dev/tty
    read -r value < /dev/tty
    printf '%s' "$value"
  fi
}

confirm() {
  local label="$1"
  local default="${2:-y}"
  local value suffix
  if [[ "$default" == "y" ]]; then
    suffix="Y/n"
  else
    suffix="y/N"
  fi
  printf '%s [%s]: ' "$label" "$suffix" > /dev/tty
  read -r value < /dev/tty
  value="${value:-$default}"
  [[ "$value" =~ ^[Yy]$ ]]
}

have() {
  command -v "$1" >/dev/null 2>&1
}

link_exists() {
  have ip && ip link show "$1" >/dev/null 2>&1
}

awg_like_interfaces() {
  have ip || return 0
  ip -o link show 2>/dev/null | awk -F': ' '
    $2 ~ /^awg[[:alnum:]_.-]*(@.*)?$/ {
      name=$2
      sub(/@.*/, "", name)
      print name
    }
  '
}

cleanup_stale_interfaces() {
  local stale=()
  local iface
  while IFS= read -r iface; do
    [[ -n "$iface" ]] || continue
    if link_exists "$iface"; then
      stale+=("$iface")
    fi
  done < <(awg_like_interfaces)
  if (( ${#stale[@]} == 0 )); then
    return
  fi
  warn "existing AWG interfaces found: ${stale[*]}"
  muted "If they are leftovers from a previous awg-forge install, remove them before starting."
  if ! confirm "Delete these interfaces now?" "y"; then
    warn "keeping existing interfaces; new install may reuse stale runtime state"
    return
  fi
  for iface in "${stale[@]}"; do
    if ip link delete "$iface" 2>/dev/null; then
      ok "deleted interface $iface"
    else
      warn "could not delete interface $iface"
    fi
  done
}

compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    printf 'docker compose'
    return
  fi
  if have docker-compose; then
    printf 'docker-compose'
    return
  fi
  return 1
}

random_hex() {
  local bytes="$1"
  if have openssl; then
    openssl rand -hex "$bytes"
    return
  fi
  od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
}

detect_route() {
  if ! have ip; then
    return
  fi
  ip route get 1.1.1.1 2>/dev/null || true
}

route_field() {
  local route="$1"
  local key="$2"
  awk -v key="$key" '{
    for (i = 1; i <= NF; i++) {
      if ($i == key && (i + 1) <= NF) {
        print $(i + 1)
        exit
      }
    }
  }' <<<"$route"
}

detect_public_ip() {
  local route="$1"
  local src
  src="$(route_field "$route" "src")"
  if [[ -n "$src" ]]; then
    printf '%s' "$src"
    return
  fi
  if have curl; then
    curl -4fsS --max-time 5 https://ifconfig.co 2>/dev/null | tr -d ' \n' || true
  fi
}

port_in_use_tcp() {
  local port="$1"
  if have ss; then
    ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .
    return
  fi
  return 1
}

port_in_use_udp() {
  local port="$1"
  if have ss; then
    ss -H -lun "sport = :$port" 2>/dev/null | grep -q .
    return
  fi
  return 1
}

validate_port() {
  local value="$1"
  [[ "$value" =~ ^[0-9]+$ ]] && (( value >= 1 && value <= 65535 ))
}

profile_from_choice() {
  case "$1" in
    1|1.0|legacy|awg_legacy_1_0) printf 'awg_legacy_1_0' ;;
    2|1.5|awg_1_5) printf 'awg_1_5' ;;
    3|2.0|awg_2_0) printf 'awg_2_0' ;;
    *) return 1 ;;
  esac
}

profile_label() {
  case "$1" in
    awg_legacy_1_0) printf 'AmneziaWG Legacy / 1.0' ;;
    awg_1_5) printf 'AmneziaWG 1.5' ;;
    awg_2_0) printf 'AmneziaWG 2.0' ;;
  esac
}

write_compose_if_missing() {
  if [[ -f "$COMPOSE_FILE" ]]; then
    ok "$COMPOSE_FILE exists"
    return
  fi
  cat >"$COMPOSE_FILE" <<'YAML'
services:
  awg-forge:
    image: ghcr.io/astronaut808/awg-forge:latest
    container_name: awg-forge
    env_file: .env
    network_mode: host
    volumes:
      - ./data:/etc/awg-forge
      - /lib/modules:/lib/modules:ro
    cap_add:
      - NET_ADMIN
      - SYS_MODULE
    devices:
      - /dev/net/tun:/dev/net/tun
    restart: unless-stopped
YAML
  ok "created $COMPOSE_FILE"
}

prepare_workdir() {
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P || true)"
  local target="${AWG_FORGE_HOME:-}"
  if [[ -z "$target" ]]; then
    if [[ -n "$script_dir" && -f "$script_dir/install.sh" && -f "$script_dir/.env.example" ]]; then
      target="$script_dir"
    else
      target="$INSTALL_DIR_DEFAULT"
    fi
  fi
  mkdir -p "$target"
  cd "$target"
  ok "working directory: $target"
}

backup_existing_env() {
  if [[ ! -f "$ENV_FILE" ]]; then
    return
  fi
  local backup="${ENV_FILE}.backup-$(date -u +%Y%m%d-%H%M%S)"
  cp "$ENV_FILE" "$backup"
  chmod 600 "$backup" || true
  warn "$ENV_FILE already exists; backup saved to $backup"
}

write_env() {
  local server_host="$1"
  local listen_port="$2"
  local webui_host="$3"
  local webui_port="$4"
  local password="$5"
  local session_secret="$6"
  local external_interface="$7"
  local ipv4_subnet="$8"
  local dns="$9"
  local allowed_ips="${10}"
  local keepalive="${11}"
  local mtu="${12}"
  local profile="${13}"

  cat >"$ENV_FILE" <<EOF
SERVER_HOST=$server_host
LISTEN_PORT=$listen_port
WEBUI_HOST=$webui_host
WEBUI_PORT=$webui_port
PASSWORD=$password
SESSION_SECRET=$session_secret
EXTERNAL_INTERFACE=$external_interface
IPV4_SUBNET=$ipv4_subnet
DNS=$dns
ALLOWED_IPS=$allowed_ips
PERSISTENT_KEEPALIVE=$keepalive
MTU=$mtu
PROTOCOL_PROFILE=$profile
APPLY_CONFIG=true
PUBLISHED_UDP_PORTS=
EOF
  chmod 600 "$ENV_FILE" || true
  ok "created $ENV_FILE"
}

doctor_has_failures() {
  grep -q '^FAIL ' "$1"
}

post_start_reconcile() {
  muted "Reconciling runtime tunnel and managed firewall rules..."
  sleep 2
  if docker exec "$APP_NAME" awg-forge tunnel restart >/tmp/awg-forge-install-restart.log 2>&1; then
    ok "runtime tunnel restarted"
  else
    warn "runtime tunnel restart reported an issue"
    cat /tmp/awg-forge-install-restart.log || true
  fi
  if docker exec "$APP_NAME" awg-forge firewall repair >/tmp/awg-forge-install-firewall.log 2>&1; then
    ok "firewall repair completed"
  else
    warn "firewall repair reported an issue"
    cat /tmp/awg-forge-install-firewall.log || true
  fi
}

print_next_steps() {
  local server_host="$1"
  local webui_host="$2"
  local webui_port="$3"
  local password="$4"
  local profile="$5"
  local compose="$6"

  printf '\n'
  bold "awg-forge is starting"
  printf '\n'
  printf 'Profile:      %s\n' "$(profile_label "$profile")"
  printf 'Web UI bind:  %s:%s\n' "$webui_host" "$webui_port"
  printf 'Password:     %s\n' "$password"
  printf 'Password file: %s\n' "$ENV_FILE"
  printf '\n'
  if [[ "$webui_host" == "127.0.0.1" || "$webui_host" == "localhost" || "$webui_host" == "::1" ]]; then
    bold "Access through SSH tunnel"
    printf 'ssh -L %s:127.0.0.1:%s user@%s\n' "$webui_port" "$webui_port" "$server_host"
    printf 'Then open: http://127.0.0.1:%s\n' "$webui_port"
  else
    warn "Web UI is bound to $webui_host. Protect it with firewall/VPN/reverse proxy."
    printf 'Open: http://%s:%s\n' "$server_host" "$webui_port"
  fi
  printf '\n'
  bold "Useful commands"
  printf '%s ps\n' "$compose"
  printf '%s logs -f\n' "$compose"
  printf 'docker exec %s awg-forge doctor\n' "$APP_NAME"
}

main() {
  bold "awg-forge quick installer"
  muted "Recommended mode: Docker host networking, Web UI bound to 127.0.0.1, access through SSH tunnel."
  printf '\n'

  if [[ "$(uname -s)" != "Linux" ]]; then
    fail "install.sh is intended for Linux servers"
    exit 1
  fi
  ok "Linux detected"

  require_tty
  prepare_workdir

  if ! have docker; then
    fail "docker is not installed"
    printf 'Install Docker first: https://docs.docker.com/engine/install/\n' >&2
    exit 1
  fi
  ok "docker found"

  if ! docker info >/dev/null 2>&1; then
    fail "docker daemon is not reachable by the current user"
    printf 'Start Docker and make sure this user can run docker commands, or run the installer with sudo.\n' >&2
    exit 1
  fi
  ok "docker daemon reachable"

  local compose
  if ! compose="$(compose_cmd)"; then
    fail "docker compose is not available"
    printf 'Install Docker Compose plugin first.\n' >&2
    exit 1
  fi
  ok "$compose found"

  if [[ -e /dev/net/tun ]]; then
    ok "/dev/net/tun exists"
  else
    warn "/dev/net/tun does not exist; container startup may fail until TUN is available"
  fi
  cleanup_stale_interfaces

  local route default_interface default_host
  route="$(detect_route)"
  default_interface="$(route_field "$route" "dev")"
  default_host="$(detect_public_ip "$route")"
  default_interface="${default_interface:-eth0}"
  default_host="${default_host:-vpn.example.com}"

  printf '\n'
  bold "Network"
  local server_host external_interface listen_port webui_host webui_port ipv4_subnet dns allowed_ips keepalive mtu
  server_host="$(prompt "Server host or public IP" "$default_host")"
  external_interface="$(prompt "External interface" "$default_interface")"
  listen_port="$(prompt "AmneziaWG UDP listen port" "51820")"
  while ! validate_port "$listen_port"; do
    warn "Port must be 1..65535"
    listen_port="$(prompt "AmneziaWG UDP listen port" "51820")"
  done
  if port_in_use_udp "$listen_port"; then
    warn "UDP port $listen_port appears to be in use"
  fi

  webui_host="$(prompt "Web UI bind host" "127.0.0.1")"
  if [[ "$webui_host" == "0.0.0.0" || "$webui_host" == "::" ]]; then
    warn "Binding Web UI publicly is risky. Use a firewall, VPN, or reverse proxy."
    confirm "Continue with public Web UI bind?" "n" || exit 1
  fi
  webui_port="$(prompt "Web UI TCP port" "51821")"
  while ! validate_port "$webui_port"; do
    warn "Port must be 1..65535"
    webui_port="$(prompt "Web UI TCP port" "51821")"
  done
  if port_in_use_tcp "$webui_port"; then
    warn "TCP port $webui_port appears to be in use"
  fi

  printf '\n'
  bold "Tunnel defaults"
  ipv4_subnet="$(prompt "IPv4 subnet" "10.8.0.0/24")"
  dns="$(prompt "DNS" "1.1.1.1")"
  allowed_ips="$(prompt "Allowed IPs" "0.0.0.0/0")"
  keepalive="$(prompt "Persistent keepalive" "0")"
  mtu="$(prompt "MTU, 0 means Auto" "0")"

  printf '\n'
  bold "Protocol profile"
  printf '1) AmneziaWG Legacy / 1.0\n'
  printf '2) AmneziaWG 1.5\n'
  printf '3) AmneziaWG 2.0\n'
  local profile_choice profile
  profile_choice="$(prompt "Choose profile" "1")"
  while ! profile="$(profile_from_choice "$profile_choice")"; do
    warn "Choose 1, 2, or 3"
    profile_choice="$(prompt "Choose profile" "1")"
  done

  printf '\n'
  bold "Security"
  local password session_secret
  password="$(random_hex 12)"
  session_secret="$(random_hex 32)"
  ok "admin password generated"
  ok "session secret generated"

  printf '\n'
  bold "Files"
  if [[ -f "$ENV_FILE" ]]; then
    warn "$ENV_FILE already exists and will be replaced after backup"
    confirm "Continue and replace $ENV_FILE?" "n" || exit 1
  fi
  backup_existing_env
  write_env "$server_host" "$listen_port" "$webui_host" "$webui_port" "$password" "$session_secret" "$external_interface" "$ipv4_subnet" "$dns" "$allowed_ips" "$keepalive" "$mtu" "$profile"
  mkdir -p "$DATA_DIR"
  chmod 700 "$DATA_DIR" || true
  ok "created $DATA_DIR/"
  write_compose_if_missing

  printf '\n'
  bold "Start Docker"
  if confirm "Pull latest image before start?" "y"; then
    $compose pull
  fi
  $compose up -d --force-recreate

  printf '\n'
  post_start_reconcile
  printf '\n'
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if docker exec "$APP_NAME" awg-forge doctor >/tmp/awg-forge-install-doctor.log 2>&1; then
      cat /tmp/awg-forge-install-doctor.log
      if ! doctor_has_failures /tmp/awg-forge-install-doctor.log; then
        ok "doctor completed"
        print_next_steps "$server_host" "$webui_host" "$webui_port" "$password" "$profile" "$compose"
        return
      fi
      warn "doctor still reports failures; retrying"
    fi
    sleep 2
  done
  if [[ -f /tmp/awg-forge-install-doctor.log ]]; then
    cat /tmp/awg-forge-install-doctor.log
  fi
  if docker exec "$APP_NAME" awg-forge doctor; then
    warn "doctor completed; inspect output above"
  else
    warn "doctor reported issues; inspect output above and run: docker exec $APP_NAME awg-forge doctor"
  fi

  print_next_steps "$server_host" "$webui_host" "$webui_port" "$password" "$profile" "$compose"
}

main "$@"

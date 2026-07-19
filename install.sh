#!/usr/bin/env bash
set -euo pipefail

APP_NAME="awg-forge"
IMAGE="${IMAGE:-ghcr.io/astronaut808/awg-forge:latest}"
INSTALL_DIR_DEFAULT="/opt/awg-forge"
ENV_FILE=".env"
COMPOSE_FILE="docker-compose.yml"
DATA_DIR="data"
INSTALL_ACTION="fresh"

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

env_value() {
  local key="$1"
  if [[ -f "$ENV_FILE" ]]; then
    awk -F= -v key="$key" '$1 == key {print substr($0, length(key) + 2); exit}' "$ENV_FILE"
  fi
}

state_path() {
  local config_dir
  config_dir="$(env_value CONFIG_DIR)"
  if [[ -n "$config_dir" && "$config_dir" != "/etc/awg-forge" ]]; then
    printf '%s/state.json' "$config_dir"
    return
  fi
  printf '%s/state.json' "$DATA_DIR"
}

state_tunnels() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  awk '
    /"interface_name":/ { iface=$2; gsub(/[",]/, "", iface) }
    /"listen_port":/ { port=$2; gsub(/,/, "", port) }
    /"ipv4_subnet":/ { subnet=$2; gsub(/[",]/, "", subnet) }
    /"enabled":/ { enabled=$2; gsub(/,/, "", enabled) }
    iface && port && subnet && enabled {
      print iface "|" port "|" subnet "|" enabled
      iface=port=subnet=enabled=""
    }
  ' "$file"
}

state_interfaces() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  awk '
    /"interface_name":/ {
      iface=$2
      gsub(/[",]/, "", iface)
      if (iface != "") print iface
    }
  ' "$file"
}

iptables_delete_all() {
  local table="$1"
  shift
  local args=("$@")
  have iptables || return 0
  while true; do
    if [[ -n "$table" ]]; then
      iptables -t "$table" -C "${args[@]}" >/dev/null 2>&1 || break
      iptables -t "$table" -D "${args[@]}" || break
    else
      iptables -C "${args[@]}" >/dev/null 2>&1 || break
      iptables -D "${args[@]}" || break
    fi
  done
}

cleanup_tunnel_rules() {
  local iface="$1"
  local port="$2"
  local subnet="$3"
  local external_interface="$4"
  [[ -n "$subnet" && -n "$external_interface" ]] && iptables_delete_all nat POSTROUTING -s "$subnet" -o "$external_interface" -j MASQUERADE
  [[ -n "$port" ]] && iptables_delete_all "" INPUT -p udp -m udp --dport "$port" -j ACCEPT
  [[ -n "$iface" ]] && iptables_delete_all "" FORWARD -i "$iface" -j ACCEPT
  [[ -n "$iface" ]] && iptables_delete_all "" FORWARD -o "$iface" -j ACCEPT
}

cleanup_interface() {
  local iface="$1"
  [[ -n "$iface" ]] || return
  if have awg-quick && [[ -f "/etc/amnezia/amneziawg/$iface.conf" ]]; then
    awg-quick down "$iface" >/dev/null 2>&1 || true
  fi
  if have ip && ip link show "$iface" >/dev/null 2>&1; then
    ip link delete "$iface" >/dev/null 2>&1 || true
  fi
}

cleanup_orphan_interfaces() {
  local state_file="${1:-}"
  local known=" "
  local iface
  if [[ -n "$state_file" && -f "$state_file" ]]; then
    while IFS= read -r iface; do
      [[ -n "$iface" ]] || continue
      known+="$iface "
    done < <(state_interfaces "$state_file")
  fi
  while IFS= read -r iface; do
    [[ -n "$iface" ]] || continue
    if [[ "$known" == *" $iface "* ]]; then
      continue
    fi
    warn "found runtime interface without state cleanup context: $iface"
    cleanup_interface "$iface"
    iptables_delete_all "" FORWARD -i "$iface" -j ACCEPT
    iptables_delete_all "" FORWARD -o "$iface" -j ACCEPT
  done < <(awg_like_interfaces)
}

cleanup_existing_runtime() {
  local state external_interface
  external_interface="$(env_value EXTERNAL_INTERFACE)"
  external_interface="${external_interface:-eth0}"
  state="$(state_path)"
  if [[ -f "$state" ]]; then
    while IFS='|' read -r iface port subnet _enabled; do
      [[ -n "$iface" ]] || continue
      warn "cleaning previous tunnel $iface"
      cleanup_tunnel_rules "$iface" "$port" "$subnet" "$external_interface"
      cleanup_interface "$iface"
    done < <(state_tunnels "$state")
    cleanup_orphan_interfaces "$state"
  else
    warn "state file not found; cleaning AWG-like runtime interfaces only"
    cleanup_orphan_interfaces
  fi
}

existing_install_found() {
  [[ -f "$ENV_FILE" || -f "$COMPOSE_FILE" || -d "$DATA_DIR" ]]
}

backup_existing_install() {
  local backup_dir
  backup_dir="reinstall-backup-$(date -u +%Y%m%d-%H%M%S)"
  mkdir -p "$backup_dir"
  local copied=false
  if [[ -f "$ENV_FILE" ]]; then
    cp -a "$ENV_FILE" "$backup_dir/"
    copied=true
  fi
  if [[ -f "$COMPOSE_FILE" ]]; then
    cp -a "$COMPOSE_FILE" "$backup_dir/"
    copied=true
  fi
  if [[ -d "$DATA_DIR" ]]; then
    cp -a "$DATA_DIR" "$backup_dir/"
    copied=true
  fi
  if $copied; then
    chmod 700 "$backup_dir" || true
    ok "backup saved to $backup_dir"
  else
    rmdir "$backup_dir" 2>/dev/null || true
  fi
}

full_reinstall() {
  local compose="$1"
  warn "full reinstall removes local state and generated configs from this install directory"
  muted "Existing clients will need fresh configs after reinstall."
  confirm "Create backup and reinstall from scratch?" "n" || exit 1
  backup_existing_install

  if [[ -n "$compose" && -f "$COMPOSE_FILE" ]]; then
    $compose down --remove-orphans || true
    ok "docker compose stopped"
  elif docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "$APP_NAME"; then
    docker rm -f "$APP_NAME" >/dev/null 2>&1 || true
    ok "container removed"
  fi

  cleanup_existing_runtime
  rm -rf "$DATA_DIR" "$ENV_FILE" "$COMPOSE_FILE"
  ok "old install files removed"
}

handle_existing_install() {
  local compose="$1"
  if ! existing_install_found; then
    return 0
  fi
  printf '\n'
  bold "Existing install"
  [[ -f "$ENV_FILE" ]] && printf '%s\n' "- $ENV_FILE"
  [[ -f "$COMPOSE_FILE" ]] && printf '%s\n' "- $COMPOSE_FILE"
  [[ -d "$DATA_DIR" ]] && printf '%s\n' "- $DATA_DIR/"
  printf '\n'
  printf '1) Reconfigure existing install, keep data and backup .env\n'
  printf '2) Full reinstall, backup and remove old data/config first\n'
  printf '3) Upgrade image, keep data and run required database migrations\n'
  printf '4) Abort\n'
  local choice
  choice="$(prompt "Choose action" "1")"
  case "$choice" in
    1) INSTALL_ACTION="reconfigure"; ok "continuing with existing data" ;;
    2) full_reinstall "$compose"; INSTALL_ACTION="fresh" ;;
    3) INSTALL_ACTION="upgrade" ;;
    4) exit 0 ;;
    *) warn "unknown choice"; handle_existing_install "$compose" ;;
  esac
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

profile_default_name() {
  case "$1" in
    awg_1_5) printf 'awg15' ;;
    awg_2_0) printf 'awg20' ;;
    *) printf 'awg0' ;;
  esac
}

profile_default_port() {
  case "$1" in
    awg_1_5) printf '51825' ;;
    awg_2_0) printf '51830' ;;
    *) printf '51820' ;;
  esac
}

profile_default_subnet() {
  case "$1" in
    awg_1_5) printf '10.15.0.0/24' ;;
    awg_2_0) printf '10.20.0.0/24' ;;
    *) printf '10.8.0.0/24' ;;
  esac
}

write_compose_if_missing() {
  if [[ -f "$COMPOSE_FILE" ]]; then
    ok "$COMPOSE_FILE exists"
    return
  fi
  cat >"$COMPOSE_FILE" <<YAML
services:
  awg-forge:
    image: $IMAGE
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

ensure_image_available() {
  if docker image inspect "$IMAGE" >/dev/null 2>&1; then
    return
  fi
  fail "$IMAGE is not available locally"
  printf 'Rerun the installer and allow image pull, or pull it manually:\n' >&2
  printf 'docker pull %s\n' "$IMAGE" >&2
  exit 1
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
  local backup
  backup="${ENV_FILE}.backup-$(date -u +%Y%m%d-%H%M%S)"
  cp "$ENV_FILE" "$backup"
  chmod 600 "$backup" || true
  warn "$ENV_FILE already exists; backup saved to $backup"
}

set_env_value() {
  local key="$1"
  local value="$2"
  local tmp
  [[ "$key" =~ ^[A-Z0-9_]+$ ]] || return 1
  [[ "$value" != *$'\n'* ]] || return 1
  if [[ ! -e "$ENV_FILE" ]]; then
    : >"$ENV_FILE"
  fi
  tmp="$(mktemp "${ENV_FILE}.tmp.XXXXXX")"
  awk -v key="$key" -v value="$value" '
    $0 ~ "^" key "=" {
      if (!found) print key "=" value
      found = 1
      next
    }
    { print }
    END { if (!found) print key "=" value }
  ' "$ENV_FILE" >"$tmp"
  mv "$tmp" "$ENV_FILE"
}

ensure_env_value() {
  local key="$1"
  local value="$2"
  if ! grep -qE "^${key}=" "$ENV_FILE" 2>/dev/null; then
    set_env_value "$key" "$value"
  fi
}

write_env() {
  local webui_host="$1"
  local webui_port="$2"
  local password="$3"
  local session_secret="$4"
  local external_interface="$5"
  local mode="${6:-fresh}"

  if [[ "$mode" == "fresh" ]]; then
    : >"$ENV_FILE"
  else
    touch "$ENV_FILE"
  fi
  set_env_value WEBUI_HOST "$webui_host"
  set_env_value WEBUI_PORT "$webui_port"
  set_env_value EXTERNAL_INTERFACE "$external_interface"
  if [[ "$mode" == "fresh" ]]; then
    set_env_value PASSWORD "$password"
    set_env_value SESSION_SECRET "$session_secret"
  else
    ensure_env_value PASSWORD "$password"
    ensure_env_value SESSION_SECRET "$session_secret"
  fi
  ensure_env_value SESSION_COOKIE_SECURE auto
  ensure_env_value WEBUI_TLS_MODE off
  ensure_env_value WEBUI_TRUST_PROXY_HEADERS false
  ensure_env_value WEBUI_TRUSTED_PROXY_CIDRS ""
  ensure_env_value APPLY_CONFIG true
  ensure_env_value PUBLISHED_UDP_PORTS ""
  ensure_env_value AUDIT_LOG_ENABLED true
  ensure_env_value AUDIT_LOG_PATH /etc/awg-forge/audit.log
  ensure_env_value AUDIT_LOG_MAX_SIZE 5242880
  ensure_env_value AUDIT_LOG_MAX_FILES 3
  if [[ "$mode" == "fresh" ]]; then
    set_env_value DATABASE_MODE sqlite
    set_env_value DATABASE_PATH /etc/awg-forge/awg-forge.db
    set_env_value DATABASE_DSN ""
    set_env_value DATABASE_RETENTION_DAYS 90
    set_env_value DATABASE_BUSY_TIMEOUT 5s
    set_env_value DATABASE_QUERY_TIMEOUT 2s
    set_env_value DATABASE_MAX_OPEN_CONNS 1
    set_env_value DATABASE_MAX_IDLE_CONNS 1
  else
    ensure_env_value DATABASE_MODE off
    ensure_env_value DATABASE_PATH /etc/awg-forge/awg-forge.db
    ensure_env_value DATABASE_DSN ""
    ensure_env_value DATABASE_RETENTION_DAYS 90
    ensure_env_value DATABASE_BUSY_TIMEOUT 5s
    ensure_env_value DATABASE_QUERY_TIMEOUT 2s
    ensure_env_value DATABASE_MAX_OPEN_CONNS 1
    ensure_env_value DATABASE_MAX_IDLE_CONNS 1
  fi
  chmod 600 "$ENV_FILE" || true
  ok "updated $ENV_FILE"
}

migrate_sqlite() {
  local compose="$1"
  if [[ "$(env_value DATABASE_MODE)" != "sqlite" ]]; then
    return 0
  fi
  muted "Applying SQLite migrations..."
  if ! $compose run --rm --no-deps awg-forge db migrate; then
    return 1
  fi
  ok "SQLite migrations applied"
}

initialize_state() {
  local server_host="$1"
  local tunnel_name="$2"
  local listen_port="$3"
  local external_interface="$4"
  local ipv4_subnet="$5"
  local dns="$6"
  local allowed_ips="$7"
  local keepalive="$8"
  local mtu="$9"
  local profile="${10}"

  ensure_image_available
  local data_dir_abs
  data_dir_abs="$(pwd -P)/$DATA_DIR"
  docker run --rm --pull=never \
    --env-file "$ENV_FILE" \
    -v "$data_dir_abs:/etc/awg-forge" \
    "$IMAGE" init \
      --server-host "$server_host" \
      --external-interface "$external_interface" \
      --profile "$profile" \
      --tunnel-name "$tunnel_name" \
      --listen-port "$listen_port" \
      --ipv4-subnet "$ipv4_subnet" \
      --dns "$dns" \
      --allowed-ips "$allowed_ips" \
      --keepalive "$keepalive" \
      --mtu "$mtu"
  ok "created $DATA_DIR/state.json"
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
  local profile_text
  profile_text="$(profile_label "$profile")"
  profile_text="${profile_text:-$profile}"

  printf '\n'
  bold "awg-forge is starting"
  printf '\n'
  printf 'Profile:      %s\n' "$profile_text"
  printf 'Web UI bind:  %s:%s\n' "$webui_host" "$webui_port"
  if [[ -n "$password" ]]; then
    printf 'Password:     %s\n' "$password"
    printf 'Password file: %s\n' "$ENV_FILE"
  fi
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

upgrade_install_is_managed() {
  [[ -f "$ENV_FILE" && -f "$COMPOSE_FILE" && -d "$DATA_DIR" ]] || return 1
  grep -Eq '^[[:space:]]*env_file:[[:space:]]*\.env[[:space:]]*$' "$COMPOSE_FILE" || return 1
  grep -Eq '^[[:space:]]*-[[:space:]]*\./data:/etc/awg-forge([[:space:]]|$)' "$COMPOSE_FILE" || return 1
  local config_dir database_path
  config_dir="$(env_value CONFIG_DIR)"
  database_path="$(env_value DATABASE_PATH)"
  [[ -z "$config_dir" || "$config_dir" == "/etc/awg-forge" ]] || return 1
  [[ -z "$database_path" || "$database_path" == "/etc/awg-forge/awg-forge.db" ]] || return 1
}

upgrade_backup() {
  local backup_dir="$1"
  mkdir -p "$backup_dir"
  cp -a "$ENV_FILE" "$backup_dir/$ENV_FILE"
  cp -a "$DATA_DIR" "$backup_dir/$DATA_DIR"
  chmod 700 "$backup_dir" || true
  ok "upgrade backup saved to $backup_dir"
}

restore_upgrade_backup() {
  local backup_dir="$1"
  [[ -f "$backup_dir/$ENV_FILE" && -d "$backup_dir/$DATA_DIR" ]] || return 1
  rm -rf "$DATA_DIR"
  cp -a "$backup_dir/$DATA_DIR" "$DATA_DIR"
  cp -a "$backup_dir/$ENV_FILE" "$ENV_FILE"
  chmod 600 "$ENV_FILE" || true
}

enable_sqlite_env() {
  set_env_value DATABASE_MODE sqlite
  ensure_env_value DATABASE_PATH /etc/awg-forge/awg-forge.db
  ensure_env_value DATABASE_DSN ""
  ensure_env_value DATABASE_RETENTION_DAYS 90
  ensure_env_value DATABASE_BUSY_TIMEOUT 5s
  ensure_env_value DATABASE_QUERY_TIMEOUT 2s
  ensure_env_value DATABASE_MAX_OPEN_CONNS 1
  ensure_env_value DATABASE_MAX_IDLE_CONNS 1
  chmod 600 "$ENV_FILE" || true
}

upgrade_verify() {
  local database_mode="$1"
  local doctor_log running
  running="$(docker inspect --format '{{.State.Running}}' "$APP_NAME" 2>/dev/null || true)"
  if [[ "$running" != "true" ]]; then
    fail "new container is not running"
    return 1
  fi
  if [[ "$database_mode" == "sqlite" ]]; then
    if ! docker exec "$APP_NAME" awg-forge db status; then
      fail "SQLite status check failed"
      return 1
    fi
  fi
  doctor_log="$(mktemp)"
  if ! docker exec "$APP_NAME" awg-forge doctor >"$doctor_log" 2>&1; then
    cat "$doctor_log" || true
    rm -f "$doctor_log"
    warn "Doctor could not complete; inspect its output before relying on the updated service"
    return 0
  fi
  cat "$doctor_log"
  if doctor_has_failures "$doctor_log"; then
    warn "Doctor reports existing or runtime issues; inspect its output"
  fi
  rm -f "$doctor_log"
}

upgrade_rollback() {
  local compose="$1"
  local backup_dir="$2"
  local previous_image="$3"
  local recreated="$4"
  local override

  warn "upgrade failed; restoring the previous installation"
  if [[ "$recreated" == "true" ]]; then
    $compose down --remove-orphans || true
  fi
  if ! restore_upgrade_backup "$backup_dir"; then
    fail "could not restore $backup_dir automatically"
    return 1
  fi
  if [[ "$recreated" == "true" ]]; then
    override="$(mktemp)"
    cat >"$override" <<EOF
services:
  awg-forge:
    image: $previous_image
EOF
    if ! $compose -f "$COMPOSE_FILE" -f "$override" up -d --force-recreate awg-forge; then
      rm -f "$override"
      fail "could not restart the previous image $previous_image"
      return 1
    fi
    rm -f "$override"
  elif ! $compose start awg-forge; then
    fail "could not restart the previous container"
    return 1
  fi
  warn "previous installation restored from $backup_dir"
}

upgrade_main() {
  bold "awg-forge upgrade"
  if [[ "$(uname -s)" != "Linux" ]]; then
    fail "install.sh is intended for Linux servers"
    return 1
  fi
  if ! have docker || ! docker info >/dev/null 2>&1; then
    fail "Docker daemon is not reachable by the current user"
    return 1
  fi
  local compose
  if ! compose="$(compose_cmd)"; then
    fail "docker compose is not available"
    return 1
  fi
  require_tty
  prepare_workdir
  if ! upgrade_install_is_managed; then
    fail "upgrade supports the managed .env + docker-compose.yml + ./data layout only"
    printf 'Use a manual upgrade for custom Compose files, CONFIG_DIR, or DATABASE_PATH.\n' >&2
    return 1
  fi
  if ! $compose config -q; then
    fail "$COMPOSE_FILE is not valid"
    return 1
  fi
  if ! docker inspect "$APP_NAME" >/dev/null 2>&1; then
    fail "container $APP_NAME was not found; refusing an upgrade without rollback context"
    return 1
  fi

  local database_mode enable_sqlite=false create_missing_sqlite=false
  database_mode="$(env_value DATABASE_MODE)"
  database_mode="${database_mode:-off}"
  case "$database_mode" in
    off)
      printf '\n'
      muted "SQLite is disabled. It enables traffic history, quotas, and indexed operational history."
      if confirm "Enable SQLite during this upgrade?" "n"; then
        enable_sqlite=true
        database_mode="sqlite"
      fi
      ;;
    sqlite)
      if [[ ! -f "$DATA_DIR/awg-forge.db" ]]; then
        warn "SQLite is enabled but $DATA_DIR/awg-forge.db is missing"
        if ! confirm "Create a new empty SQLite database during this upgrade?" "n"; then
          warn "upgrade cancelled before the running installation was changed"
          return 1
        fi
        create_missing_sqlite=true
      fi
      ;;
    postgres)
      fail "DATABASE_MODE=postgres is not supported by the upgrade path"
      return 1
      ;;
    *)
      fail "unsupported DATABASE_MODE=$database_mode"
      return 1
      ;;
  esac

  local previous_image backup_dir recreated=false
  previous_image="$(docker inspect --format '{{.Image}}' "$APP_NAME")"
  backup_dir="$(mktemp -d "upgrade-backup-$(date -u +%Y%m%d-%H%M%S).XXXXXX")"

  muted "Pulling the target image..."
  if ! $compose pull awg-forge; then
    fail "could not pull the target image; the running installation was not changed"
    return 1
  fi
  if ! $compose stop awg-forge; then
    fail "could not stop the current container"
    return 1
  fi
  if ! upgrade_backup "$backup_dir"; then
    $compose start awg-forge || true
    fail "could not create an upgrade backup"
    return 1
  fi
  if $enable_sqlite || $create_missing_sqlite; then
    enable_sqlite_env
  fi
  if [[ "$database_mode" == "sqlite" ]] && ! migrate_sqlite "$compose"; then
    upgrade_rollback "$compose" "$backup_dir" "$previous_image" "$recreated" || true
    return 1
  fi

  recreated=true
  if ! $compose up -d --force-recreate awg-forge; then
    upgrade_rollback "$compose" "$backup_dir" "$previous_image" "$recreated" || true
    return 1
  fi
  if ! upgrade_verify "$database_mode"; then
    upgrade_rollback "$compose" "$backup_dir" "$previous_image" "$recreated" || true
    return 1
  fi
  ok "upgrade completed"
  if [[ "$database_mode" == "sqlite" ]]; then
    ok "SQLite is enabled and migrated"
  fi
}

main() {
  if [[ "${1:-}" == "upgrade" ]]; then
    if (( $# != 1 )); then
      fail "usage: install.sh upgrade"
      exit 1
    fi
    upgrade_main
    return
  fi
  if (( $# != 0 )); then
    fail "usage: install.sh [upgrade]"
    exit 1
  fi
  bold "awg-forge quick installer"
  muted "Recommended mode: Docker host networking, Web UI bound to 127.0.0.1, access through SSH tunnel."
  printf '\n'

  if [[ "$(uname -s)" != "Linux" ]]; then
    fail "install.sh is intended for Linux servers"
    exit 1
  fi
  ok "Linux detected"

  if ! have docker; then
    fail "docker is not installed"
    printf 'Install Docker Engine first: https://docs.docker.com/engine/install/\n' >&2
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

  require_tty
  prepare_workdir

  if [[ -e /dev/net/tun ]]; then
    ok "/dev/net/tun exists"
  else
    warn "/dev/net/tun does not exist; container startup may fail until TUN is available"
  fi

  handle_existing_install "$compose"
  if [[ "$INSTALL_ACTION" == "upgrade" ]]; then
    upgrade_main
    return
  fi
  cleanup_stale_interfaces

  local existing_state=false
  if [[ -f "$(state_path)" ]]; then
    existing_state=true
  fi

  local route default_interface default_host
  route="$(detect_route)"
  default_interface="$(route_field "$route" "dev")"
  default_host="$(detect_public_ip "$route")"
  default_interface="${default_interface:-eth0}"
  default_host="${default_host:-vpn.example.com}"

  printf '\n'
  bold "Network"
  local server_host external_interface webui_host webui_port
  server_host="$default_host"
  if ! $existing_state; then
    server_host="$(prompt "Server host or public IP" "$default_host")"
  else
    ok "existing state.json found; tunnel settings will be kept"
  fi
  local existing_external_interface existing_webui_host existing_webui_port
  existing_external_interface="$(env_value EXTERNAL_INTERFACE)"
  existing_webui_host="$(env_value WEBUI_HOST)"
  existing_webui_port="$(env_value WEBUI_PORT)"
  external_interface="$(prompt "External interface" "${existing_external_interface:-$default_interface}")"

  webui_host="$(prompt "Web UI bind host" "${existing_webui_host:-127.0.0.1}")"
  if [[ "$webui_host" == "0.0.0.0" || "$webui_host" == "::" ]]; then
    warn "Binding Web UI publicly is risky. Use a firewall, VPN, or reverse proxy."
    confirm "Continue with public Web UI bind?" "n" || exit 1
  fi
  webui_port="$(prompt "Web UI TCP port" "${existing_webui_port:-51821}")"
  while ! validate_port "$webui_port"; do
    warn "Port must be 1..65535"
    webui_port="$(prompt "Web UI TCP port" "${existing_webui_port:-51821}")"
  done
  if port_in_use_tcp "$webui_port"; then
    warn "TCP port $webui_port appears to be in use"
  fi

  local profile="existing state"
  local tunnel_name="" listen_port="" ipv4_subnet="" dns="" allowed_ips="" keepalive="" mtu=""
  if ! $existing_state; then
    printf '\n'
    bold "Protocol profile"
    printf '1) AmneziaWG Legacy / 1.0\n'
    printf '2) AmneziaWG 1.5\n'
    printf '3) AmneziaWG 2.0\n'
    local profile_choice
    profile_choice="$(prompt "Choose profile" "3")"
    while ! profile="$(profile_from_choice "$profile_choice")"; do
      warn "Choose 1, 2, or 3"
      profile_choice="$(prompt "Choose profile" "3")"
    done

    printf '\n'
    bold "Tunnel defaults"
    tunnel_name="$(prompt "Tunnel name / interface" "$(profile_default_name "$profile")")"
    listen_port="$(prompt "AmneziaWG UDP listen port" "$(profile_default_port "$profile")")"
    while ! validate_port "$listen_port"; do
      warn "Port must be 1..65535"
      listen_port="$(prompt "AmneziaWG UDP listen port" "$(profile_default_port "$profile")")"
    done
    if port_in_use_udp "$listen_port"; then
      warn "UDP port $listen_port appears to be in use"
    fi
    ipv4_subnet="$(prompt "IPv4 subnet" "$(profile_default_subnet "$profile")")"
    dns="$(prompt "DNS" "1.1.1.1")"
    allowed_ips="$(prompt "Allowed IPs" "0.0.0.0/0")"
    keepalive="$(prompt "Persistent keepalive" "0")"
    mtu="$(prompt "MTU, 0 means Auto" "0")"
  fi

  printf '\n'
  bold "Security"
  local password session_secret password_generated=false
  password="$(env_value PASSWORD)"
  session_secret="$(env_value SESSION_SECRET)"
  if [[ -z "$password" ]]; then
    password="$(random_hex 12)"
    password_generated=true
    ok "admin password generated"
  else
    ok "existing admin password kept"
  fi
  if [[ -z "$session_secret" ]]; then
    session_secret="$(random_hex 32)"
    ok "session secret generated"
  else
    ok "existing session secret kept"
  fi

  printf '\n'
  bold "Files"
  if [[ -f "$ENV_FILE" ]]; then
    warn "$ENV_FILE already exists; changed runtime values will be backed up and updated"
    confirm "Continue and update $ENV_FILE?" "n" || exit 1
  fi
  backup_existing_env
  write_env "$webui_host" "$webui_port" "$password" "$session_secret" "$external_interface" "$INSTALL_ACTION"
  mkdir -p "$DATA_DIR"
  chmod 700 "$DATA_DIR" || true
  ok "created $DATA_DIR/"
  write_compose_if_missing

  printf '\n'
  bold "Prepare Docker image"
  if confirm "Pull $IMAGE before initialization/start?" "y"; then
    docker pull "$IMAGE"
  fi

  if ! $existing_state; then
    printf '\n'
    bold "Initialize state"
    initialize_state "$server_host" "$tunnel_name" "$listen_port" "$external_interface" "$ipv4_subnet" "$dns" "$allowed_ips" "$keepalive" "$mtu" "$profile"
  fi

  migrate_sqlite "$compose"

  printf '\n'
  bold "Start Docker"
  $compose up -d --force-recreate

  printf '\n'
  post_start_reconcile
  printf '\n'
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if docker exec "$APP_NAME" awg-forge doctor >/tmp/awg-forge-install-doctor.log 2>&1; then
      cat /tmp/awg-forge-install-doctor.log
      if ! doctor_has_failures /tmp/awg-forge-install-doctor.log; then
        ok "doctor completed"
        if $password_generated; then
          print_next_steps "$server_host" "$webui_host" "$webui_port" "$password" "$profile" "$compose"
        else
          print_next_steps "$server_host" "$webui_host" "$webui_port" "" "$profile" "$compose"
        fi
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

  if $password_generated; then
    print_next_steps "$server_host" "$webui_host" "$webui_port" "$password" "$profile" "$compose"
  else
    print_next_steps "$server_host" "$webui_host" "$webui_port" "" "$profile" "$compose"
  fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

#!/usr/bin/env bash
set -euo pipefail

APP_NAME="awg-forge"
INSTALL_DIR_DEFAULT="/opt/awg-forge"
ENV_FILE=".env"
COMPOSE_FILE="docker-compose.yml"
DATA_DIR="data"

YES=false
PURGE=false
DRY_RUN=false

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
muted() { printf '\033[2m%s\033[0m\n' "$*"; }
ok() { printf '\033[32mOK\033[0m   %s\n' "$*"; }
warn() { printf '\033[33mWARN\033[0m %s\n' "$*"; }
fail() { printf '\033[31mERR\033[0m  %s\n' "$*" >&2; }

usage() {
  cat <<'EOF'
Usage: uninstall.sh [--yes] [--purge] [--dry-run]

Stops awg-forge, removes AWG runtime interfaces, and deletes managed firewall
rules. Data is kept by default.

Options:
  --yes       Do not prompt for confirmation.
  --purge     Remove .env, data/, and docker-compose.yml after shutdown.
  --dry-run   Print actions without changing the system.
  --help      Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --yes|-y) YES=true ;;
    --purge) PURGE=true ;;
    --dry-run) DRY_RUN=true ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1"; usage; exit 1 ;;
  esac
  shift
done

confirm() {
  local label="$1"
  local default="${2:-n}"
  local value suffix
  if $YES; then
    return 0
  fi
  if [[ ! -r /dev/tty ]]; then
    fail "confirmation requires a TTY; pass --yes for non-interactive uninstall"
    exit 1
  fi
  if [[ "$default" == "y" ]]; then suffix="Y/n"; else suffix="y/N"; fi
  printf '%s [%s]: ' "$label" "$suffix" > /dev/tty
  read -r value < /dev/tty
  value="${value:-$default}"
  [[ "$value" =~ ^[Yy]$ ]]
}

have() { command -v "$1" >/dev/null 2>&1; }

run() {
  if $DRY_RUN; then
    printf 'DRY '
    printf '%q' "$1"
    shift || true
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

compose_cmd() {
  if have docker && docker compose version >/dev/null 2>&1; then
    printf 'docker compose'
    return
  fi
  if have docker-compose; then
    printf 'docker-compose'
    return
  fi
  return 1
}

prepare_workdir() {
  local script_dir target
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P || true)"
  target="${AWG_FORGE_HOME:-}"
  if [[ -z "$target" ]]; then
    if [[ -n "$script_dir" && -f "$script_dir/$COMPOSE_FILE" ]]; then
      target="$script_dir"
    else
      target="$INSTALL_DIR_DEFAULT"
    fi
  fi
  if [[ ! -d "$target" ]]; then
    fail "install directory not found: $target"
    exit 1
  fi
  cd "$target"
  ok "working directory: $target"
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

iptables_delete_all() {
  local table="$1"
  shift
  local args=("$@")
  have iptables || return 0
  while true; do
    if [[ -n "$table" ]]; then
      iptables -t "$table" -C "${args[@]}" >/dev/null 2>&1 || break
      run iptables -t "$table" -D "${args[@]}" || break
    else
      iptables -C "${args[@]}" >/dev/null 2>&1 || break
      run iptables -D "${args[@]}" || break
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
    run awg-quick down "$iface" >/dev/null 2>&1 || true
  fi
  if have ip && ip link show "$iface" >/dev/null 2>&1; then
    run ip link delete "$iface" >/dev/null 2>&1 || true
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

main() {
  bold "awg-forge uninstall"
  prepare_workdir
  confirm "Stop awg-forge and remove runtime interfaces/firewall rules?" "n" || exit 1

  local compose=""
  compose="$(compose_cmd || true)"
  local external_interface
  external_interface="$(env_value EXTERNAL_INTERFACE)"
  external_interface="${external_interface:-eth0}"

  local state
  state="$(state_path)"
  if [[ -f "$state" ]]; then
    while IFS='|' read -r iface port subnet enabled; do
      [[ -n "$iface" ]] || continue
      warn "cleaning tunnel $iface"
      cleanup_tunnel_rules "$iface" "$port" "$subnet" "$external_interface"
      cleanup_interface "$iface"
    done < <(state_tunnels "$state")
    cleanup_orphan_interfaces "$state"
  else
    warn "state file not found; cleaning AWG-like runtime interfaces only"
    cleanup_orphan_interfaces
  fi

  if [[ -n "$compose" && -f "$COMPOSE_FILE" ]]; then
    $compose down --remove-orphans || true
    ok "docker compose stopped"
  elif have docker; then
    docker rm -f "$APP_NAME" >/dev/null 2>&1 || true
    ok "container removed if it existed"
  fi

  if $PURGE || confirm "Remove .env, data/, and docker-compose.yml?" "n"; then
    run rm -rf "$DATA_DIR" "$ENV_FILE" "$COMPOSE_FILE"
    ok "local install files removed"
  else
    ok "kept local data and .env"
  fi
  ok "uninstall completed"
}

main "$@"

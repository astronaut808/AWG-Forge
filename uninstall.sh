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
REMOVE_ORPHANS=false

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
muted() { printf '\033[2m%s\033[0m\n' "$*"; }
ok() { printf '\033[32mOK\033[0m   %s\n' "$*"; }
warn() { printf '\033[33mWARN\033[0m %s\n' "$*"; }
fail() { printf '\033[31mERR\033[0m  %s\n' "$*" >&2; }

usage() {
  cat <<'EOF'
Usage: uninstall.sh [--yes] [--purge] [--dry-run] [--remove-orphans]

Stops awg-forge, removes AWG runtime interfaces, and deletes managed firewall
rules. Data is kept by default.

Options:
  --yes       Do not prompt for confirmation.
  --purge     Remove .env, data/, and docker-compose.yml after shutdown.
  --dry-run   Print actions without changing the system.
  --remove-orphans
              Also remove AWG-like runtime interfaces missing from state.json.
  --help      Show this help.
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --yes|-y) YES=true ;;
      --purge) PURGE=true ;;
      --dry-run) DRY_RUN=true ;;
      --remove-orphans) REMOVE_ORPHANS=true ;;
      --help|-h) usage; exit 0 ;;
      *) fail "unknown option: $1"; usage; exit 1 ;;
    esac
    shift
  done
}

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

run_compose() {
  local compose="$1"
  shift
  local -a command
  read -r -a command <<<"$compose"
  run "${command[@]}" "$@"
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
  if [[ -f "$DATA_DIR/state.json" ]]; then
    printf '%s/state.json' "$DATA_DIR"
    return
  fi
  config_dir="$(env_value CONFIG_DIR)"
  if [[ -n "$config_dir" && "$config_dir" != "/etc/awg-forge" && -f "$config_dir/state.json" ]]; then
    printf '%s/state.json' "$config_dir"
    return
  fi
  printf '%s/state.json' "$DATA_DIR"
}

state_external_interface() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  awk -F'"' '/"external_interface":/ { print $4; exit }' "$file"
}

state_tunnels() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  awk '
    /"tunnels": \[/ { in_tunnels=1; next }
    in_tunnels && depth == 0 && /^[[:space:]]*\]/ { exit }
    !in_tunnels { next }
    /^[[:space:]]*\{/ {
      depth++
      if (depth == 1) id=iface=port=subnet=egress=enabled=""
      next
    }
    depth == 1 && /"id":/ { id=$2; gsub(/[",]/, "", id) }
    depth == 1 && /"interface_name":/ { iface=$2; gsub(/[",]/, "", iface) }
    depth == 1 && /"listen_port":/ { port=$2; gsub(/,/, "", port) }
    depth == 1 && /"ipv4_subnet":/ { subnet=$2; gsub(/[",]/, "", subnet) }
    depth == 1 && /"egress_mode":/ { egress=$2; gsub(/[",]/, "", egress) }
    depth == 1 && /"enabled":/ { enabled=$2; gsub(/,/, "", enabled) }
    /^[[:space:]]*\},?$/ {
      if (depth == 1 && iface && port && subnet && enabled) {
        print id "|" iface "|" port "|" subnet "|" egress "|" enabled
      }
      depth--
    }
  ' "$file"
}

state_warp_interface() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  awk -F'"' '
    /"warp":/ { in_warp=1; next }
    in_warp && /"tunnels":/ { exit }
    in_warp && /"interface_name":/ { iface=$4 }
    in_warp && /"private_key":/ { configured=1 }
    END {
      if (configured) print (iface == "" ? "warp0" : iface)
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
  if $DRY_RUN; then
    if [[ -n "$table" ]]; then
      iptables -t "$table" -C "${args[@]}" >/dev/null 2>&1 || return 0
      run iptables -t "$table" -D "${args[@]}"
    else
      iptables -C "${args[@]}" >/dev/null 2>&1 || return 0
      run iptables -D "${args[@]}"
    fi
    return 0
  fi
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

ip_rule_delete_all() {
  local subnet="$1"
  have ip || return 0
  if $DRY_RUN; then
    run ip rule del from "$subnet" lookup 200
    return 0
  fi
  while ip rule del from "$subnet" lookup 200 >/dev/null 2>&1; do
    :
  done
}

cleanup_warp_route() {
  local iface="$1"
  local subnet="$2"
  [[ -n "$iface" && -n "$subnet" ]] || return 0
  ip_rule_delete_all "$subnet"
  if have ip; then
    if $DRY_RUN; then
      run ip route del "$subnet" dev "$iface" table 200
    else
      ip route del "$subnet" dev "$iface" table 200 >/dev/null 2>&1 || true
    fi
  fi
}

cleanup_tunnel_rules() {
  local tunnel_id="$1"
  local iface="$2"
  local port="$3"
  local subnet="$4"
  local external_interface="$5"
  [[ -n "$subnet" && -n "$external_interface" ]] && iptables_delete_all nat POSTROUTING -s "$subnet" -o "$external_interface" -j MASQUERADE
  [[ -n "$subnet" && "$external_interface" != "warp0" ]] && iptables_delete_all nat POSTROUTING -s "$subnet" -o warp0 -j MASQUERADE
  [[ -n "$port" ]] && iptables_delete_all "" INPUT -p udp -m udp --dport "$port" -j ACCEPT
  [[ -n "$iface" ]] && iptables_delete_all "" FORWARD -i "$iface" -j ACCEPT
  [[ -n "$iface" ]] && iptables_delete_all "" FORWARD -o "$iface" -j ACCEPT
  cleanup_tagged_tunnel_rules "$tunnel_id" "$iface" "$port" "$subnet" "$external_interface"
}

cleanup_tagged_tunnel_rules() {
  local tunnel_id="$1"
  local iface="$2"
  local port="$3"
  local subnet="$4"
  local external_interface="$5"
  local tag egress
  local -a egresses
  [[ -n "$tunnel_id" && -n "$iface" && -n "$port" && -n "$subnet" && -n "$external_interface" ]] || return 0
  if [[ ! "$tunnel_id" =~ ^[A-Za-z0-9_-]+$ ]] || (( ${#tunnel_id} > 220 )); then
    warn "cannot derive managed firewall tags for tunnel $iface from an invalid state ID"
    return 0
  fi
  tag="awg-forge-$tunnel_id"
  egresses=("$external_interface")
  if [[ "$external_interface" != "warp0" ]]; then
    egresses+=("warp0")
  fi
  iptables_delete_all "" INPUT -p udp -m udp --dport "$port" -m comment --comment "$tag-input-udp" -j ACCEPT
  for egress in "${egresses[@]}"; do
    iptables_delete_all nat POSTROUTING -s "$subnet" -o "$egress" -m comment --comment "$tag-masquerade" -j MASQUERADE
    iptables_delete_all "" FORWARD -i "$iface" -s "$subnet" -o "$egress" -m conntrack --ctstate NEW,ESTABLISHED,RELATED -m comment --comment "$tag-forward-egress" -j ACCEPT
    iptables_delete_all "" FORWARD -i "$egress" -o "$iface" -d "$subnet" -m conntrack --ctstate ESTABLISHED,RELATED -m comment --comment "$tag-forward-return" -j ACCEPT
  done
}

cleanup_interface() {
  local iface="$1"
  [[ -n "$iface" ]] || return
  if have awg-quick && [[ -f "/etc/amnezia/amneziawg/$iface.conf" ]]; then
    if $DRY_RUN; then
      run awg-quick down "$iface"
    else
      awg-quick down "$iface" >/dev/null 2>&1 || true
    fi
  fi
  if have ip && ip link show "$iface" >/dev/null 2>&1; then
    if $DRY_RUN; then
      run ip link delete "$iface"
    else
      ip link delete "$iface" >/dev/null 2>&1 || true
    fi
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
  local state
  state="$(state_path)"

  if [[ -n "$compose" && -f "$COMPOSE_FILE" ]]; then
    run_compose "$compose" down --remove-orphans || true
    ok "docker compose stopped"
  elif have docker; then
    if $DRY_RUN; then
      run docker rm -f "$APP_NAME"
    else
      docker rm -f "$APP_NAME" >/dev/null 2>&1 || true
    fi
    ok "container removed if it existed"
  fi

  local external_interface
  external_interface="$(state_external_interface "$state")"
  external_interface="${external_interface:-$(env_value EXTERNAL_INTERFACE)}"
  external_interface="${external_interface:-eth0}"

  if [[ -f "$state" ]]; then
    local warp_interface=""
    while IFS='|' read -r tunnel_id iface port subnet egress _enabled; do
      [[ -n "$iface" ]] || continue
      warn "cleaning tunnel $iface"
      cleanup_tunnel_rules "$tunnel_id" "$iface" "$port" "$subnet" "$external_interface"
      if [[ "$egress" == "warp" ]]; then
        cleanup_warp_route "$iface" "$subnet"
        warp_interface="${warp_interface:-$(state_warp_interface "$state")}"
        warp_interface="${warp_interface:-warp0}"
      fi
      cleanup_interface "$iface"
    done < <(state_tunnels "$state")
    if [[ -n "$warp_interface" ]]; then
      cleanup_interface "$warp_interface"
    fi
    if $REMOVE_ORPHANS; then
      cleanup_orphan_interfaces "$state"
    fi
  else
    warn "state file not found; exact managed interfaces and firewall rules cannot be determined"
    if $REMOVE_ORPHANS; then
      cleanup_orphan_interfaces
    else
      warn "leaving AWG-like interfaces untouched; use --remove-orphans only after reviewing them"
    fi
  fi

  if $PURGE || confirm "Remove .env, data/, and docker-compose.yml?" "n"; then
    run rm -rf "$DATA_DIR" "$ENV_FILE" "$COMPOSE_FILE"
    ok "local install files removed"
  else
    ok "kept local data and .env"
  fi
  ok "uninstall completed"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  parse_args "$@"
  main
fi

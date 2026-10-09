#!/usr/bin/env bash
set -euo pipefail

APP_NAME="awg-forge"
IMAGE="${IMAGE:-ghcr.io/astronaut808/awg-forge:latest}"
INSTALL_DIR_DEFAULT="/opt/awg-forge"
ENV_FILE=".env"
COMPOSE_FILE="docker-compose.yml"
DATA_DIR="data"
INSTALL_ACTION="fresh"
DEFAULT_TUNNEL_UDP_PORT_RANGE="30000-49999"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
muted() { printf '\033[2m%s\033[0m\n' "$*"; }
ok() { printf '\033[32mOK\033[0m   %s\n' "$*"; }
warn() { printf '\033[33mWARN\033[0m %s\n' "$*"; }
fail() { printf '\033[31mERR\033[0m  %s\n' "$*" >&2; }

require_tty() {
  if ! (: <> /dev/tty) 2>/dev/null; then
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
    printf '%s [%s]: ' "$label" "$default" > /dev/tty || return 1
    read -r value < /dev/tty || return 1
    printf '%s' "${value:-$default}"
  else
    printf '%s: ' "$label" > /dev/tty || return 1
    read -r value < /dev/tty || return 1
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
  printf '%s [%s]: ' "$label" "$suffix" > /dev/tty || return 1
  read -r value < /dev/tty || return 1
  value="${value:-$default}"
  [[ "$value" =~ ^[Yy]$ ]]
}

have() {
  command -v "$1" >/dev/null 2>&1
}

running_as_root() {
  (( EUID == 0 ))
}

selinux_volume_suffix() {
  if [[ -e /sys/fs/selinux/enforce ]]; then
    printf ':Z'
  fi
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
  if have docker-compose && docker-compose version >/dev/null 2>&1; then
    printf 'docker-compose'
    return
  fi
  return 1
}

detect_distribution() {
  DISTRO_ID="unknown"
  DISTRO_VERSION=""
  DISTRO_CODENAME=""
  PACKAGE_FAMILY=""
  DOCKER_REPO_DISTRO=""
  [[ -r /etc/os-release ]] || return 0
  local ID="" VERSION_ID="" VERSION_CODENAME=""
  # os-release is trusted host configuration, never installation input.
  # shellcheck disable=SC1091
  source /etc/os-release
  DISTRO_ID="$ID"
  DISTRO_VERSION="$VERSION_ID"
  DISTRO_CODENAME="$VERSION_CODENAME"
  case "$DISTRO_ID:$DISTRO_VERSION" in
    ubuntu:*|debian:*) PACKAGE_FAMILY=apt; DOCKER_REPO_DISTRO="$DISTRO_ID" ;;
    centos:9|centos:10) PACKAGE_FAMILY=rpm; DOCKER_REPO_DISTRO=centos ;;
    rhel:8*|rhel:9*|rhel:10*) PACKAGE_FAMILY=rpm; DOCKER_REPO_DISTRO=rhel ;;
  esac
}

dependency_bootstrap_supported() {
  case "$DISTRO_ID:$DISTRO_VERSION:$DISTRO_CODENAME" in
    ubuntu:22.04:jammy|ubuntu:24.04:noble|ubuntu:26.04:resolute|debian:12:bookworm|debian:13:trixie)
      have apt-get && have dpkg-query && have systemctl ;;
    centos:9:*|centos:10:*|rhel:8:*|rhel:8.*:*|rhel:9:*|rhel:9.*:*|rhel:10:*|rhel:10.*:*)
      have dnf && have rpm && have systemctl ;;
    *) return 1 ;;
  esac
}

missing_host_packages() {
  have curl || printf '%s\n' curl
  [[ -s /etc/ssl/certs/ca-certificates.crt || -s /etc/pki/tls/certs/ca-bundle.crt ]] || printf '%s\n' ca-certificates
  have ip && have ss || { if [[ "$PACKAGE_FAMILY" == rpm ]]; then printf '%s\n' iproute; else printf '%s\n' iproute2; fi; }
  have iptables || printf '%s\n' iptables
  have openssl || printf '%s\n' openssl
  have modprobe || printf '%s\n' kmod
  have awk || printf '%s\n' gawk
}

apt_install_missing() {
  # Refuse removals and preserve versions of explicitly requested installed packages.
  apt-get install -y --no-install-recommends --no-remove --no-upgrade "$@"
}

check_docker_package_conflicts() {
  local package status
  if [[ "$PACKAGE_FAMILY" == rpm ]]; then
    for package in docker docker-client docker-client-latest docker-common docker-latest docker-latest-logrotate docker-logrotate docker-engine podman podman-docker runc containerd; do
      if rpm -q "$package" >/dev/null 2>&1; then
        fail "conflicting package $package is installed; resolve Docker package conflicts manually"
        return 1
      fi
    done
    return 0
  fi
  for package in docker.io docker-compose docker-compose-v2 docker-doc docker-buildx podman-docker containerd runc; do
    status="$(dpkg-query -W -f='${Status}' "$package" 2>/dev/null || true)"
    if [[ "$status" == "install ok installed" ]]; then
      fail "conflicting package $package is installed; resolve Docker package conflicts manually"
      return 1
    fi
  done
}

install_docker_packages() {
  local install_engine="$1"
  local -a packages=(docker-compose-plugin)
  if [[ "$install_engine" == "true" ]]; then
    check_docker_package_conflicts || return 1
    packages=(docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin)
  fi

  if [[ "$PACKAGE_FAMILY" == rpm ]]; then
    local repo=/etc/yum.repos.d/awg-forge-docker.repo
    if ! dnf -q list --available "${packages[0]}" >/dev/null 2>&1; then
      if [[ -e "$repo" || -L "$repo" ]]; then
        fail "Docker repository already exists but packages are unavailable; check $repo manually"
        return 1
      fi
      local tmp
      tmp="$(mktemp)" || return 1
      if ! curl -fsSL --connect-timeout 10 --max-time 60 "https://download.docker.com/linux/$DOCKER_REPO_DISTRO/docker-ce.repo" -o "$tmp" ||
        ! install -m 0644 "$tmp" "$repo"; then
        rm -f "$tmp"
        return 1
      fi
      rm -f "$tmp"
    fi
    # Never enable allowerasing: a conflict must stop rather than remove another runtime.
    dnf install -y --setopt=install_weak_deps=False "${packages[@]}"
    return
  fi

  # Reuse the operator's repositories where these packages are already available.
  local candidate
  candidate="$(apt-cache policy "${packages[0]}" | awk '/Candidate:/ {print $2; exit}')"
  if [[ -z "$candidate" || "$candidate" == "(none)" ]]; then
    local key=/etc/apt/keyrings/awg-forge-docker.asc
    local repo=/etc/apt/sources.list.d/awg-forge-docker.sources
    if [[ -e "$key" || -L "$key" || -e "$repo" || -L "$repo" ]]; then
      fail "Docker repository files already exist but packages are unavailable; check $repo manually"
      return 1
    fi
    local tmp
    tmp="$(mktemp -d)" || return 1
    if ! curl -fsSL --connect-timeout 10 --max-time 60 "https://download.docker.com/linux/$DISTRO_ID/gpg" -o "$tmp/docker.asc"; then
      rm -rf "$tmp"
      return 1
    fi
    cat >"$tmp/docker.sources" <<EOF
Types: deb
URIs: https://download.docker.com/linux/$DISTRO_ID
Suites: $DISTRO_CODENAME
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: $key
EOF
    if ! install -m 0755 -d /etc/apt/keyrings ||
      ! install -m 0644 "$tmp/docker.asc" "$key" ||
      ! install -m 0644 "$tmp/docker.sources" "$repo"; then
      rm -rf "$tmp"
      return 1
    fi
    rm -rf "$tmp"
    apt-get update || return 1
  fi
  apt_install_missing "${packages[@]}"
}

ensure_host_dependencies() {
  detect_distribution
  ok "distribution: $DISTRO_ID ${DISTRO_VERSION:-unknown version}"
  local install_engine=false install_compose=false
  have docker || install_engine=true
  compose_cmd >/dev/null || install_compose=true
  local -a packages=()
  local package
  while IFS= read -r package; do
    packages+=("$package")
  done < <(missing_host_packages)

  if $install_engine || $install_compose || (( ${#packages[@]} > 0 )); then
    if ! dependency_bootstrap_supported; then
      fail "automatic preparation supports Ubuntu 22.04/24.04/26.04, Debian 12/13, CentOS Stream 9/10 and RHEL 8/9/10 with systemd"
      printf 'Prepare Docker, Compose and host tools manually: https://docs.docker.com/engine/install/\n' >&2
      return 1
    fi
    if ! running_as_root; then
      fail "dependency installation requires root; rerun with sudo ./install.sh"
      return 1
    fi
    muted "Missing host packages: ${packages[*]:-none}; Docker Engine: $install_engine; Compose: $install_compose"
    confirm "Install missing dependencies using the package manager and Docker's official repository if needed?" "y" || return 1
    # Check before even installing utility packages on hosts with conflicting runtimes.
    if $install_engine; then
      check_docker_package_conflicts || return 1
    fi
    if [[ "$PACKAGE_FAMILY" == rpm ]]; then
      if (( ${#packages[@]} > 0 )); then
        dnf install -y --setopt=install_weak_deps=False "${packages[@]}" || return 1
      fi
    else
      apt-get update || return 1
      if (( ${#packages[@]} > 0 )); then
        apt_install_missing "${packages[@]}" || return 1
      fi
    fi
    if $install_engine || $install_compose; then
      install_docker_packages "$install_engine" || return 1
    fi
  fi

  if [[ -n "$(missing_host_packages)" ]]; then
    fail "required host tools are still missing after preparation"
    return 1
  fi
  if ! docker info >/dev/null 2>&1; then
    if ! running_as_root || ! have systemctl; then
      fail "Docker daemon is not reachable; start Docker and run the installer with sudo"
      return 1
    fi
    # Do not start a different local daemon when the operator targets a remote context.
    if [[ -n "${DOCKER_HOST:-}" || -n "${DOCKER_CONTEXT:-}" ]] || [[ "$(docker context show)" != "default" ]]; then
      fail "selected Docker context is not reachable; check it manually"
      return 1
    fi
    confirm "Start the local Docker service?" "y" || return 1
    systemctl enable --now docker || return 1
  fi
  if ! docker info >/dev/null 2>&1 || ! compose_cmd >/dev/null; then
    fail "Docker or Compose is still unavailable after preparation"
    return 1
  fi
  ensure_tun_device || return 1
  ok "Docker, Compose and TUN are ready"
}

ensure_tun_device() {
  if [[ ! -c /dev/net/tun ]] && running_as_root && have modprobe; then
    modprobe tun || true
  fi
  if [[ ! -c /dev/net/tun ]]; then
    fail "/dev/net/tun is unavailable; enable TUN in the kernel or VPS provider settings"
    return 1
  fi
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

random_u32() {
  od -An -N4 -tu4 /dev/urandom | tr -d ' \n'
}

random_available_udp_port() {
  local range="${1:-$DEFAULT_TUNNEL_UDP_PORT_RANGE}"
  local first="${range%-*}"
  local last="${range#*-}"
  if ! validate_port "$first" || ! validate_port "$last" || (( first > last )); then
    return 1
  fi
  local count=$((last - first + 1))
  local start=$(( $(random_u32) % count ))
  local offset candidate
  for ((offset = 0; offset < count; offset++)); do
    candidate=$((first + (start + offset) % count))
    if (( candidate < 1024 )); then
      continue
    fi
    case "$candidate" in
      53|123|443|500|3478|4500|5349) continue ;;
    esac
    if ! port_in_use_udp "$candidate"; then
      printf '%s' "$candidate"
      return 0
    fi
  done
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

normalize_acme_domain() {
  local domain="$1" label
  domain="${domain#"${domain%%[![:space:]]*}"}"
  domain="${domain%"${domain##*[![:space:]]}"}"
  domain="$(printf '%s' "$domain" | tr '[:upper:]' '[:lower:]')"
  domain="${domain%.}"

  [[ -n "$domain" && ${#domain} -le 253 && "$domain" == *.* && "$domain" != *'*'* ]] || return 1
  [[ ! "$domain" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1

  local -a labels
  IFS=. read -r -a labels <<<"$domain"
  for label in "${labels[@]}"; do
    [[ ${#label} -ge 1 && ${#label} -le 63 ]] || return 1
    [[ "$label" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || return 1
  done
  printf '%s' "$domain"
}

is_loopback_webui_host() {
  local host
  host="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "$host" in
    ""|127.0.0.1|::1|localhost) return 0 ;;
  esac
  return 1
}

acme_ip_default_webui_host() {
  if [[ "$1" == *:* ]]; then
    printf '::'
    return
  fi
  printf '0.0.0.0'
}

acme_ip_webui_host_matches() {
  local certificate_ip="$1"
  local webui_host="$2"
  local wildcard
  wildcard="$(acme_ip_default_webui_host "$certificate_ip")"
  [[ "$webui_host" == "$wildcard" || "$webui_host" == "$certificate_ip" ]]
}

managed_acme_tls_configured() {
  local settings_path="$DATA_DIR/tls/config.json"
  [[ -f "$settings_path" ]] || return 1
  grep -Eq '"mode"[[:space:]]*:[[:space:]]*"acme-(domain|ip)"' "$settings_path"
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
  local volume_suffix
  volume_suffix="$(selinux_volume_suffix)"
  cat >"$COMPOSE_FILE" <<YAML
services:
  awg-forge:
    image: $IMAGE
    container_name: awg-forge
    env_file: .env
    network_mode: host
    volumes:
      - ./data:/etc/awg-forge$volume_suffix
      - /lib/modules:/lib/modules:ro
    cap_add:
      - NET_ADMIN
      - SYS_MODULE
    devices:
      - /dev/net/tun:/dev/net/tun
    logging:
      driver: local
      options:
        max-size: "10m"
        max-file: "3"
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
  ensure_env_value WEBUI_TRUST_PROXY_HEADERS false
  ensure_env_value WEBUI_TRUSTED_PROXY_CIDRS ""
  ensure_env_value APPLY_CONFIG true
  ensure_env_value PUBLISHED_UDP_PORTS ""
  if [[ "$mode" == "fresh" ]]; then
    set_env_value TUNNEL_UDP_PORT_RANGE "$DEFAULT_TUNNEL_UDP_PORT_RANGE"
  fi
  ensure_env_value AUDIT_LOG_ENABLED true
  ensure_env_value AUDIT_LOG_PATH /etc/awg-forge/audit.log
  ensure_env_value AUDIT_LOG_MAX_SIZE 5242880
  ensure_env_value AUDIT_LOG_MAX_FILES 3
  ensure_env_value LOG_LEVEL info
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
  local volume_suffix
  volume_suffix="$(selinux_volume_suffix)"
  docker run --rm --pull=never \
    --env-file "$ENV_FILE" \
    -v "$data_dir_abs:/etc/awg-forge$volume_suffix" \
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

configure_tls() {
  local compose="$1"
  local mode="$2"
  local domain="$3"
  local email="$4"
  local ip="${5:-}"

  if [[ "$mode" == "acme-domain" ]]; then
    $compose run --rm --no-deps awg-forge tls use acme-domain \
      --domain "$domain" \
      --email "$email" \
      --accept-tos
  elif [[ "$mode" == "acme-ip" ]]; then
    $compose run --rm --no-deps awg-forge tls use acme-ip \
      --ip "$ip" \
      --email "$email" \
      --accept-tos
  else
    $compose run --rm --no-deps awg-forge tls disable
  fi
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
    printf 'Password file: %s/%s\n' "$(pwd -P)" "$ENV_FILE"
  fi
  printf '\n'
  local tls_mode="off" scheme="http" access_host="$server_host"
  if [[ -f "$DATA_DIR/tls/config.json" ]]; then
    tls_mode="$(sed -nE 's/.*"mode"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "$DATA_DIR/tls/config.json" | head -n 1)"
  fi
  case "$tls_mode" in
    acme-domain|acme-ip|manual) scheme="https" ;;
    reverse-proxy) scheme="" ;;
  esac
  if [[ "$tls_mode" == "acme-ip" ]]; then
    access_host="$(sed -nE 's/.*"acme_ip"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "$DATA_DIR/tls/config.json" | head -n 1)"
    access_host="${access_host:-$server_host}"
    if [[ "$access_host" == *:* && "$access_host" != \[*\] ]]; then
      access_host="[$access_host]"
    fi
  fi

  if [[ "$webui_host" == "127.0.0.1" || "$webui_host" == "localhost" || "$webui_host" == "::1" ]]; then
    bold "Access through SSH tunnel"
    printf 'ssh -L %s:127.0.0.1:%s user@%s\n' "$webui_port" "$webui_port" "$server_host"
    if [[ -n "$scheme" ]]; then
      printf 'Then open: %s://127.0.0.1:%s\n' "$scheme" "$webui_port"
    else
      printf 'Then open the HTTPS URL configured in your reverse proxy.\n'
    fi
  else
    case "$tls_mode" in
      off)
        warn "Web UI is bound to $webui_host over HTTP. Protect it with firewall/VPN/reverse proxy."
        ;;
      acme-domain|acme-ip|manual)
        muted "Web UI is publicly available over HTTPS. Restrict administrative access further if needed."
        ;;
    esac
    if [[ -n "$scheme" ]]; then
      printf 'Open: %s://%s:%s\n' "$scheme" "$access_host" "$webui_port"
    else
      printf 'Open the HTTPS URL configured in your reverse proxy.\n'
    fi
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
  grep -Eq '^[[:space:]]*-[[:space:]]*\./data:/etc/awg-forge(:Z)?([[:space:]]|$)' "$COMPOSE_FILE" || return 1
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

# Node onboarding deliberately has no dependency on the interactive installer
# below.  It is safe to invoke from a downloaded, verified copy of this script
# and never writes an existing installation outside the application itself.
join_usage() {
  cat >&2 <<'USAGE'
usage: install.sh join|rebind --workdir DIR --image IMAGE --script-sha256 SHA256 \
  --artifact-version VERSION --artifact-commit SHA40 --controller-url URL \
  --ca-pin sha256:PIN --invitation-id UUID --name NAME [options]

Options: --secret-fd 0, --timeout DURATION, --mode fresh|existing, --fresh,
         --container NAME, --maintenance (acknowledges service downtime), --start-service, --confirm-node-id UUID,
         --confirm-controller-id UUID
USAGE
}

join_fail() { fail "$*"; return 1; }

join_is_sha256() { [[ "$1" =~ ^[a-f0-9]{64}$ ]]; }
join_is_commit() { [[ "$1" =~ ^[a-f0-9]{40}$ ]]; }
join_is_version() { [[ "$1" =~ ^(v[0-9]+\.[0-9]+\.[0-9]+|local-[a-f0-9]{12})$ ]]; }
join_is_image() {
  [[ "$1" =~ ^ghcr\.io/astronaut808/awg-forge@sha256:[a-f0-9]{64}$ || "$1" =~ ^sha256:[a-f0-9]{64}$ ]]
}

join_clean_up() {
  local status=$?
  local helper_id='' remaining='' helper_gone=true
  set +e
  if [[ -n "${JOIN_CIDFILE:-}" && -f "$JOIN_CIDFILE" ]]; then
    helper_id="$(<"$JOIN_CIDFILE")"
    if [[ "$helper_id" =~ ^[a-f0-9]{64}$ ]]; then
      if ! timeout --foreground -k 5s 15s docker rm -f "$helper_id" >/dev/null 2>&1; then
        remaining="$(timeout --foreground -k 5s 10s docker ps -a --no-trunc --filter "id=$helper_id" --format '{{.ID}}' 2>/dev/null)" || helper_gone=false
        [[ -z "$remaining" ]] || helper_gone=false
      fi
    fi
  fi
  if [[ -n "${JOIN_TMPDIR:-}" ]]; then
    rm -rf "$JOIN_TMPDIR"
  fi
  if [[ "${JOIN_STOP_ATTEMPTED:-false}" == true && "${JOIN_WAS_RUNNING:-false}" == true && "${JOIN_COMMITTED:-false}" != true && -n "${JOIN_CONTAINER:-}" ]]; then
    if "$helper_gone"; then
      timeout --foreground -k 5s 45s docker start "$JOIN_CONTAINER" >/dev/null 2>&1 || warn "previous service could not be resumed; inspect the retained identity and journal before retrying start"
    else
      warn "helper termination is uncertain; service remains stopped for inspection"
    fi
  fi
  if [[ "$status" != 0 && "${JOIN_FRESH_WORKDIR:-}" != '' ]]; then
    warn "fresh workdir retained for offline identity/journal inspection; do not replay a consumed invitation"
  fi
  exit "$status"
}

join_timeout_seconds() {
  local value="$1" number unit seconds
  [[ "$value" =~ ^([1-9][0-9]*)([smh])$ ]] || return 1
  number="${BASH_REMATCH[1]}"; unit="${BASH_REMATCH[2]}"
  (( ${#number} <= 3 )) || return 1
  case "$unit" in s) seconds="$number" ;; m) seconds=$((number * 60)) ;; h) seconds=$((number * 3600)) ;; esac
  (( seconds > 0 && seconds <= 900 )) || return 1
  printf '%s' "$seconds"
}

join_signal() {
  if [[ "${JOIN_HELPER_LAUNCHING:-false}" == true ]]; then
    JOIN_SIGNAL_STATUS="$1"
    return
  fi
  if [[ "${JOIN_HELPER_PID:-}" =~ ^[0-9]+$ ]]; then
    kill -TERM "$JOIN_HELPER_PID" 2>/dev/null || true
  fi
  exit "$1"
}

# Create before attach: an interrupted create can leave only a stopped helper,
# never an enrollment process whose CID is not yet known. Explicit stdin
# forwarding and builtin wait keep PID-only signals cancellable without losing
# the private FD channel to background-job /dev/null defaults.
join_run_helper() {
  local deadline="$1" helper_id status=0
  shift
  timeout --foreground -k 5s 30s docker create "$@" >/dev/null || return 1
  helper_id="$(<"$JOIN_CIDFILE")"
  [[ "$helper_id" =~ ^[a-f0-9]{64}$ ]] || return 1
  JOIN_HELPER_LAUNCHING=true
  timeout --foreground -k 5s "$deadline" docker start -ai "$helper_id" <&0 &
  JOIN_HELPER_PID=$!
  JOIN_HELPER_LAUNCHING=false
  if [[ "$JOIN_SIGNAL_STATUS" != 0 ]]; then join_signal "$JOIN_SIGNAL_STATUS"; fi
  wait "$JOIN_HELPER_PID" || status=$?
  JOIN_HELPER_PID=''
  return "$status"
}

join_open_tty() {
  exec {JOIN_TTY}<>/dev/tty
}

join_require_unlabelled_docker() {
  local security_options
  security_options="$(timeout --foreground -k 5s 10s docker info --format '{{range .SecurityOptions}}{{println .}}{{end}}')" || return 1
  if grep -Eq '^name=selinux([[:space:],]|$)' <<<"$security_options"; then
    join_fail "installer join/rebind does not support Docker SELinux labels; use a deployment-specific offline workflow"
    return 1
  fi
}

join_require_prerequisites() {
  [[ "$(uname -s)" == Linux ]] || { join_fail "join requires Linux"; return 1; }
  [[ "${EUID:-$(id -u)}" -eq 0 ]] || { join_fail "join requires root"; return 1; }
  have docker || { join_fail "docker is not installed"; return 1; }
  have timeout || { join_fail "join requires the timeout utility"; return 1; }
  timeout --foreground -k 5s 15s docker info >/dev/null 2>&1 || { join_fail "docker daemon is not reachable"; return 1; }
  timeout --foreground -k 5s 15s docker compose version >/dev/null 2>&1 || { join_fail "docker compose is not available"; return 1; }
  join_require_unlabelled_docker || return 1
}

join_verify_script() {
  local expected="$1" actual
  [[ -f "${BASH_SOURCE[0]}" ]] || { join_fail "join requires a downloaded script file"; return 1; }
  actual="$(sha256sum "${BASH_SOURCE[0]}" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || { join_fail "script SHA-256 does not match --script-sha256"; return 1; }
}

join_verify_artifact() {
  local image="$1" version="$2" commit="$3" endpoint="$4" pin="$5" invitation="$6" name="$7" node_id="${8:-}" controller_id="${9:-}"
  local -a checker=(node installer-check --artifact-version "$version" --artifact-commit "$commit" --controller-url "$endpoint" --ca-pin "$pin" --invitation-id "$invitation" --name "$name")
  [[ -n "$node_id" ]] && checker+=(--confirm-node-id "$node_id")
  [[ -n "$controller_id" ]] && checker+=(--confirm-controller-id "$controller_id")
  timeout --foreground -k 5s 15s docker image inspect "$image" >/dev/null 2>&1 || { join_fail "verified image is not available locally"; return 1; }
  timeout --foreground -k 5s 30s docker run --rm "$image" "${checker[@]}" | grep -qx 'installer-onboarding-v1 compatible' ||
    { join_fail "image does not support this installer artifact"; return 1; }
}

join_container_is_owned() {
  local container="$1" workdir="$2" image="$3" container_id actual_workdir service actual_image expected_image user userns process_label mount_label
  container_id="$(timeout --foreground -k 5s 10s docker inspect --format '{{.Id}}' "$container" 2>/dev/null)" || return 1
  [[ "$container_id" =~ ^[a-f0-9]{64}$ ]] || return 1
  actual_workdir="$(timeout --foreground -k 5s 10s docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$container_id" 2>/dev/null)" || return 1
  service="$(timeout --foreground -k 5s 10s docker inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$container_id" 2>/dev/null)" || return 1
  actual_image="$(timeout --foreground -k 5s 10s docker inspect --format '{{.Image}}' "$container_id" 2>/dev/null)" || return 1
  expected_image="$(timeout --foreground -k 5s 10s docker image inspect --format '{{.Id}}' "$image" 2>/dev/null)" || return 1
  [[ "$actual_workdir" == "$workdir" && "$service" == awg-forge && "$actual_image" == "$expected_image" ]] || return 1
  user="$(timeout --foreground -k 5s 10s docker inspect --format '{{.Config.User}}' "$container_id" 2>/dev/null)" || return 1
  userns="$(timeout --foreground -k 5s 10s docker inspect --format '{{.HostConfig.UsernsMode}}' "$container_id" 2>/dev/null)" || return 1
  [[ -z "$user" || "$user" == root || "$user" == 0 || "$user" == 0:0 || "$user" == root:root ]] || return 1
  [[ -z "$userns" ]] || return 1
  process_label="$(timeout --foreground -k 5s 10s docker inspect --format '{{.ProcessLabel}}' "$container_id" 2>/dev/null)" || return 1
  mount_label="$(timeout --foreground -k 5s 10s docker inspect --format '{{.MountLabel}}' "$container_id" 2>/dev/null)" || return 1
  [[ -z "$process_label" && -z "$mount_label" ]] || return 1
  printf '%s\n' "$container_id"
}

join_capture_container_env() {
  local container="$1" env_file="$2"
  umask 077
  timeout --foreground -k 5s 10s docker inspect --format '{{range .Config.Env}}{{printf "%q\n" .}}{{end}}' "$container" >"$env_file.quoted" || return 1
  if grep -Eq '\\(n|r|x|u)' "$env_file.quoted"; then
    join_fail "container environment contains unsafe control characters"
    return 1
  fi
  timeout --foreground -k 5s 10s docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container" >"$env_file" || return 1
  awk '$0 != "" && $0 !~ /^[A-Za-z_][A-Za-z0-9_]*=/ { exit 1 }' "$env_file" || { rm -f "$env_file"; join_fail "container environment cannot be safely forwarded"; return 1; }
  chmod 600 "$env_file"
}

join_write_fresh_files() {
  local workdir="$1" image="$2" container="$3" password session_secret
  [[ ! -e "$workdir" ]] || { join_fail "--fresh requires an empty, non-existent --workdir"; return 1; }
  umask 077
  mkdir -m 700 "$workdir" || return 1
  mkdir -m 700 "$workdir/data" || return 1
  chmod 700 "$workdir" "$workdir/data"
  password="$(random_hex 24)"
  session_secret="$(random_hex 32)"
  cat >"$workdir/.env" <<EOF
WEBUI_HOST=127.0.0.1
WEBUI_PORT=51821
PASSWORD=$password
SESSION_SECRET=$session_secret
DATABASE_MODE=off
APPLY_CONFIG=true
EOF
  chmod 600 "$workdir/.env"
  cat >"$workdir/docker-compose.yml" <<EOF
services:
  awg-forge:
    image: $image
    container_name: $container
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
EOF
  chmod 600 "$workdir/docker-compose.yml"
}

join_run() {
  set -euo pipefail
  set +x
  local action="$1"
  shift
  local workdir='' image='' script_sha='' version='' commit='' endpoint='' pin='' invitation='' name=''
  local secret_fd='' timeout='10m' timeout_seconds='' fresh=false container="$APP_NAME" maintenance=false start_service=false node_id='' controller_id='' mode=''
  local JOIN_TMPDIR='' JOIN_CIDFILE='' JOIN_CONTAINER='' JOIN_WAS_RUNNING=false JOIN_COMMITTED=false JOIN_TTY='' JOIN_STOP_ATTEMPTED=false JOIN_FRESH_WORKDIR='' JOIN_HELPER_PID='' JOIN_HELPER_LAUNCHING=false JOIN_SIGNAL_STATUS=0
  while (( $# )); do
    case "$1" in
      --workdir|--image|--script-sha256|--artifact-version|--artifact-commit|--controller-url|--ca-pin|--invitation-id|--name|--secret-fd|--timeout|--container|--confirm-node-id|--confirm-controller-id|--mode)
        (( $# >= 2 )) || { join_usage; return 2; }
        case "$1" in
          --workdir) workdir="$2" ;; --image) image="$2" ;; --script-sha256) script_sha="$2" ;;
          --artifact-version) version="$2" ;; --artifact-commit) commit="$2" ;; --controller-url) endpoint="$2" ;;
          --ca-pin) pin="$2" ;; --invitation-id) invitation="$2" ;; --name) name="$2" ;;
          --secret-fd) secret_fd="$2" ;; --timeout) timeout="$2" ;; --container) container="$2" ;;
          --confirm-node-id) node_id="$2" ;; --confirm-controller-id) controller_id="$2" ;;
          --mode) mode="$2" ;;
        esac
        shift 2 ;;
      --fresh) fresh=true; shift ;;
      --maintenance) maintenance=true; shift ;;
      --start-service) start_service=true; shift ;;
      --help|-h) join_usage; return 0 ;;
      *) join_usage; return 2 ;;
    esac
  done
  [[ -n "$workdir" && -n "$image" && -n "$script_sha" && -n "$version" && -n "$commit" && -n "$endpoint" && -n "$pin" && -n "$invitation" && -n "$name" ]] || { join_usage; return 2; }
  join_is_sha256 "$script_sha" && join_is_commit "$commit" && join_is_version "$version" && join_is_image "$image" || { join_fail "invalid verified artifact metadata"; return 1; }
  [[ "$pin" =~ ^sha256:[a-f0-9]{64}$ && "$container" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || { join_fail "invalid pin or container name"; return 1; }
  [[ -z "$secret_fd" || "$secret_fd" == 0 ]] || { join_fail "--secret-fd only supports stdin (0)"; return 1; }
  [[ -z "$mode" || "$mode" == fresh || "$mode" == existing ]] || { join_fail "--mode must be fresh or existing"; return 1; }
  [[ "$mode" != fresh || "$fresh" == true ]] || fresh=true
  [[ "$mode" != existing || "$fresh" == false ]] || { join_fail "--fresh conflicts with --mode existing"; return 1; }
  [[ "$action" != join || ( -z "$node_id" && -z "$controller_id" ) ]] || { join_fail "join does not accept recovery confirmations"; return 1; }
  [[ "$action" != rebind || ( -n "$node_id" && -n "$controller_id" ) ]] || { join_fail "rebind requires both current identity confirmations"; return 1; }
  [[ "$action" != rebind || "$fresh" == false ]] || { join_fail "rebind applies to an existing node only"; return 1; }
  timeout_seconds="$(join_timeout_seconds "$timeout")" || { join_fail "timeout must be 1s..15m"; return 1; }
  join_require_prerequisites || return 1
  join_verify_script "$script_sha" || return 1
  join_verify_artifact "$image" "$version" "$commit" "$endpoint" "$pin" "$invitation" "$name" "$node_id" "$controller_id" || return 1
  if [[ -z "$secret_fd" ]]; then
    join_open_tty || { join_fail "secret requires a TTY or --secret-fd 0"; return 1; }
  fi
  if "$fresh"; then
    local fresh_parent fresh_base
    fresh_parent="$(cd "$(dirname "$workdir")" && pwd -P)" || { join_fail "fresh workdir parent does not exist"; return 1; }
    fresh_base="$(basename "$workdir")"
    [[ "$fresh_base" != . && "$fresh_base" != .. && ! -L "$workdir" && ! -e "$workdir" ]] || { join_fail "--fresh requires a non-existent direct child workdir"; return 1; }
    workdir="$fresh_parent/$fresh_base"
  else
    workdir="$(cd "$workdir" && pwd -P)" || { join_fail "existing workdir is unavailable"; return 1; }
  fi
  local node_action=enroll
  [[ "$action" == rebind ]] && node_action=rebind
  local -a node_args=(node "$node_action" --controller-url "$endpoint" --ca-pin "$pin" --invitation-id "$invitation" --name "$name" --timeout "$timeout" --secret-fd 0)
  if [[ "$action" == rebind ]]; then node_args+=(--confirm-node-id "$node_id" --confirm-controller-id "$controller_id"); fi
  JOIN_TMPDIR="$(mktemp -d /tmp/awg-forge-join.XXXXXX)" || return 1
  JOIN_CIDFILE="$JOIN_TMPDIR/helper.cid"
  JOIN_CONTAINER="$container"
  trap join_clean_up EXIT
  trap 'join_signal 129' HUP
  trap 'join_signal 130' INT
  trap 'join_signal 143' TERM
  if "$fresh"; then
    join_write_fresh_files "$workdir" "$image" "$container" || exit 1
    JOIN_FRESH_WORKDIR="$workdir"
    if [[ -n "$secret_fd" ]]; then
      join_run_helper "$((timeout_seconds + 30))s" --rm --cidfile "$JOIN_CIDFILE" -i --network host --env-file "$workdir/.env" -v "$workdir/data:/etc/awg-forge" "$image" "${node_args[@]}" || exit 1
    else
      join_run_helper "$((timeout_seconds + 30))s" --rm --cidfile "$JOIN_CIDFILE" -it --network host --env-file "$workdir/.env" -v "$workdir/data:/etc/awg-forge" "$image" "${node_args[@]}" <&"$JOIN_TTY" || exit 1
    fi
    JOIN_COMMITTED=true
    timeout --foreground -k 5s 45s docker compose -f "$workdir/docker-compose.yml" --project-directory "$workdir" up -d awg-forge || { join_fail "enrollment committed; service start is pending"; exit 1; }
    ok "enrollment committed; controller connectivity is pending until presence is observed"
    exit 0
  fi
  JOIN_CONTAINER="$(join_container_is_owned "$container" "$workdir" "$image")" || { join_fail "container is not the requested root Compose service with supported user namespace and exact artifact"; exit 1; }
  if [[ "$(timeout --foreground -k 5s 10s docker inspect --format '{{.State.Running}}' "$JOIN_CONTAINER")" == true ]]; then
    JOIN_WAS_RUNNING=true
  fi
  "$maintenance" || { join_fail "existing installation requires --maintenance before stop"; exit 1; }
  join_capture_container_env "$JOIN_CONTAINER" "$JOIN_TMPDIR/env" || exit 1
  if "$JOIN_WAS_RUNNING"; then
    JOIN_STOP_ATTEMPTED=true
    timeout --foreground -k 5s 40s docker stop -t 30 "$JOIN_CONTAINER" >/dev/null || exit 1
  fi
  if [[ -n "$secret_fd" ]]; then
    join_run_helper "$((timeout_seconds + 30))s" --rm --cidfile "$JOIN_CIDFILE" -i --network host --volumes-from "$JOIN_CONTAINER" --env-file "$JOIN_TMPDIR/env" "$image" "${node_args[@]}" || exit 1
  else
    join_run_helper "$((timeout_seconds + 30))s" --rm --cidfile "$JOIN_CIDFILE" -it --network host --volumes-from "$JOIN_CONTAINER" --env-file "$JOIN_TMPDIR/env" "$image" "${node_args[@]}" <&"$JOIN_TTY" || exit 1
  fi
  JOIN_COMMITTED=true
  if "$JOIN_WAS_RUNNING" || "$start_service"; then
    timeout --foreground -k 5s 45s docker start "$JOIN_CONTAINER" >/dev/null || { join_fail "enrollment committed; service start is pending"; exit 1; }
  fi
  ok "enrollment committed; controller connectivity is pending until presence is observed"
  exit 0
}

main() {
  if [[ "${1:-}" == join || "${1:-}" == rebind ]]; then
    local join_action="$1"
    shift
    join_run "$join_action" "$@"
    return
  fi
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
  if [[ "$(uname -m)" != "x86_64" && "$IMAGE" == ghcr.io/astronaut808/awg-forge:* ]]; then
    fail "official images currently support x86_64 only; use a compatible custom IMAGE for this architecture"
    exit 1
  fi

  require_tty
  ensure_host_dependencies || exit 1
  local compose
  compose="$(compose_cmd)"
  ok "$compose found"
  prepare_workdir

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
  local server_host external_interface webui_host webui_port tls_mode tls_acme_domain tls_acme_ip tls_acme_email
  server_host="$default_host"
  if ! $existing_state; then
    if [[ "$default_host" != "vpn.example.com" ]]; then
      muted "Detected outbound address: $default_host. Verify it is the public endpoint; NAT or floating IP can differ."
    fi
    server_host="$(prompt "Server host or public IP" "$default_host")"
  else
    ok "existing state.json found; tunnel settings will be kept"
  fi
  local existing_external_interface existing_webui_host existing_webui_port
  existing_external_interface="$(env_value EXTERNAL_INTERFACE)"
  existing_webui_host="$(env_value WEBUI_HOST)"
  existing_webui_port="$(env_value WEBUI_PORT)"
  external_interface="$(prompt "External interface" "${existing_external_interface:-$default_interface}")"

  tls_mode="off"
  tls_acme_domain=""
  tls_acme_ip=""
  tls_acme_email=""
  local webui_default_host="${existing_webui_host:-127.0.0.1}"
  if [[ "$INSTALL_ACTION" == "fresh" ]]; then
    printf '\n'
    bold "Web UI TLS"
    printf '1) HTTP for loopback or SSH tunnel (default)\n'
    printf '2) ACME certificate for a public DNS domain with automatic renewal (HTTP-01 on TCP/80)\n'
    printf '3) Short-lived ACME certificate for a public IP with automatic renewal (HTTP-01 on TCP/80)\n'
    local tls_choice
    tls_choice="$(prompt "Choose TLS mode" "1")"
    while [[ "$tls_choice" != "1" && "$tls_choice" != "2" && "$tls_choice" != "3" ]]; do
      warn "Choose 1, 2, or 3"
      tls_choice="$(prompt "Choose TLS mode" "1")"
    done
    case "$tls_choice" in
      1)
        webui_default_host="127.0.0.1"
        ;;
      2)
        tls_mode="acme-domain"
        while ! tls_acme_domain="$(normalize_acme_domain "$(prompt "Public DNS domain for the Web UI")")"; do
          warn "Enter a public DNS name, for example panel.example.com"
        done
        webui_default_host="0.0.0.0"
        ;;
      3)
        tls_mode="acme-ip"
        tls_acme_ip="$(prompt "Public IPv4 or IPv6 address for the Web UI" "${default_host:-}")"
        [[ -n "$tls_acme_ip" ]] || { fail "public IP address is required"; exit 1; }
        webui_default_host="$(acme_ip_default_webui_host "$tls_acme_ip")"
        ;;
    esac
  else
    muted "Existing TLS settings will be kept. Use 'awg-forge tls status' to inspect them."
  fi

  printf '\n'
  bold "Web UI"
  webui_host="$(prompt "Web UI bind host" "$webui_default_host")"
  while [[ "$tls_mode" == "acme-domain" ]] && is_loopback_webui_host "$webui_host"; do
    warn "ACME requires a publicly reachable Web UI bind."
    webui_host="$(prompt "Web UI bind host" "0.0.0.0")"
  done
  while [[ "$tls_mode" == "acme-ip" ]] && ! acme_ip_webui_host_matches "$tls_acme_ip" "$webui_host"; do
    warn "ACME IP TLS requires its matching wildcard bind or the exact certificate IP."
    webui_host="$(prompt "Web UI bind host" "$(acme_ip_default_webui_host "$tls_acme_ip")")"
  done
  if ! is_loopback_webui_host "$webui_host"; then
    if [[ "$tls_mode" == "off" ]]; then
      warn "The Web UI will be publicly reachable over HTTP. HTTPS is recommended."
    else
      muted "The Web UI will be publicly reachable over HTTPS. Restrict administration with a firewall or VPN when possible."
    fi
    confirm "Continue with this public Web UI bind?" "n" || exit 1
  fi
  webui_port="$(prompt "Web UI TCP port" "${existing_webui_port:-51821}")"
  while ! validate_port "$webui_port"; do
    warn "Port must be 1..65535"
    webui_port="$(prompt "Web UI TCP port" "${existing_webui_port:-51821}")"
  done
  if port_in_use_tcp "$webui_port"; then
    warn "TCP port $webui_port appears to be in use"
  fi
  if [[ "$INSTALL_ACTION" == "fresh" && ( "$tls_mode" == "acme-domain" || "$tls_mode" == "acme-ip" ) ]]; then
    tls_acme_email="$(prompt "ACME contact email")"
    [[ -n "$tls_acme_email" ]] || { fail "ACME contact email is required"; exit 1; }
    warn "The selected public identifier must reach this host and TCP/80 must be reachable before startup."
    confirm "Accept the certificate authority terms of service?" "n" || exit 1
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
    printf '1) Choose a free UDP port automatically (%s)\n' "$DEFAULT_TUNNEL_UDP_PORT_RANGE"
    printf '2) Enter a UDP port manually\n'
    local port_choice
    port_choice="$(prompt "UDP port selection" "1")"
    while [[ "$port_choice" != "1" && "$port_choice" != "2" ]]; do
      warn "Choose 1 or 2"
      port_choice="$(prompt "UDP port selection" "1")"
    done
    if [[ "$port_choice" == "1" ]]; then
      if ! listen_port="$(random_available_udp_port "$DEFAULT_TUNNEL_UDP_PORT_RANGE")"; then
        fail "no free UDP port is available in $DEFAULT_TUNNEL_UDP_PORT_RANGE"
        exit 1
      fi
      ok "selected UDP port $listen_port"
    else
      listen_port="$(prompt "AmneziaWG UDP listen port" "$(profile_default_port "$profile")")"
      while ! validate_port "$listen_port"; do
        warn "Port must be 1..65535"
        listen_port="$(prompt "AmneziaWG UDP listen port" "$(profile_default_port "$profile")")"
      done
      if port_in_use_udp "$listen_port"; then
        warn "UDP port $listen_port appears to be in use"
      fi
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
  if is_loopback_webui_host "$webui_host" && managed_acme_tls_configured; then
    warn "Managed ACME TLS cannot run with a loopback Web UI bind."
    confirm "Disable managed ACME TLS before restricting Web UI access?" "n" || exit 1
    if ! $compose run --rm --no-deps awg-forge tls disable; then
      fail "could not disable managed ACME TLS; Web UI bind was not changed"
      exit 1
    fi
    ok "managed ACME TLS disabled"
  fi
  backup_existing_env
  write_env "$webui_host" "$webui_port" "$password" "$session_secret" "$external_interface" "$INSTALL_ACTION"
  if [[ "$INSTALL_ACTION" == "fresh" ]]; then
    chmod 600 "$ENV_FILE" || true
  fi
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

    printf '\n'
    bold "Configure Web UI TLS"
    if ! configure_tls "$compose" "$tls_mode" "$tls_acme_domain" "$tls_acme_email" "$tls_acme_ip"; then
      fail "could not save Web UI TLS configuration"
      exit 1
    fi
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

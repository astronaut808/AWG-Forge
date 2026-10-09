#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

run_case() (
  source "$repo_root/install.sh"
  local scenario="$1" log="$test_dir/$1.log"
  local engine=false compose=false tools=false daemon=false root=true tun=true
  local packages_fail=false update_fail=false denied=false conflict=false remote=false
  local expected=success
  : >"$log"
  case "$scenario" in
    fresh) ;;
    ready) engine=true; compose=true; tools=true; daemon=true ;;
    compose-only) engine=true; tools=true; daemon=true ;;
    tools-only) engine=true; compose=true; daemon=true ;;
    stopped) engine=true; compose=true; tools=true ;;
    unsupported) expected=failure ;;
    unsupported-ready) engine=true; compose=true; tools=true; daemon=true ;;
    unprivileged) root=false; expected=failure ;;
    declined) denied=true; expected=failure ;;
    confirmation-read-failure) expected=failure ;;
    conflicting-runtime) conflict=true; expected=failure ;;
    package-failure) packages_fail=true; expected=failure ;;
    update-failure) update_fail=true; expected=failure ;;
    no-tun) engine=true; compose=true; tools=true; daemon=true; tun=false; expected=failure ;;
    remote-context) engine=true; compose=true; tools=true; remote=true; expected=failure ;;
    service-failure) engine=true; compose=true; tools=true; expected=failure ;;
    centos|rhel) ;;
    rpm-compose-only) engine=true; tools=true; daemon=true ;;
    rpm-conflict) conflict=true; expected=failure ;;
    rpm-package-failure) packages_fail=true; expected=failure ;;
  esac
  detect_distribution() {
    DISTRO_ID=ubuntu; DISTRO_VERSION=24.04; DISTRO_CODENAME=noble
    PACKAGE_FAMILY=apt; DOCKER_REPO_DISTRO=ubuntu
    if [[ "$scenario" == unsupported* ]]; then DISTRO_ID=alpine; fi
    if [[ "$scenario" == centos || "$scenario" == rhel || "$scenario" == rpm-* ]]; then
      DISTRO_ID=rhel; DISTRO_VERSION=9.6; DISTRO_CODENAME=""
      PACKAGE_FAMILY=rpm; DOCKER_REPO_DISTRO=rhel
      if [[ "$scenario" == centos ]]; then DISTRO_ID=centos; DISTRO_VERSION=9; DOCKER_REPO_DISTRO=centos; fi
    fi
  }
  running_as_root() { "$root"; }
  have() {
    case "$1" in
      docker) "$engine" ;;
      docker-compose) return 1 ;;
      *) return 0 ;;
    esac
  }
  missing_host_packages() {
    if ! $tools; then printf '%s\n' iproute2 kmod; fi
  }
  docker() {
    case "$*" in
      'compose version') "$compose" ;;
      info) "$engine" && "$daemon" ;;
      'context show') if $remote; then printf remote; else printf default; fi ;;
      *) printf 'unexpected Docker command: %s\n' "$*" >&2; return 1 ;;
    esac
  }
  confirm() { printf 'confirm %s\n' "$*" >>"$log"; ! "$denied"; }
  dpkg-query() { if $conflict && [[ "$*" == *containerd ]]; then printf 'install ok installed'; fi; }
  rpm() { "$conflict" && [[ "$*" == *podman ]]; }
  apt-cache() { printf '  Candidate: (none)\n'; }
  dpkg() { printf 'amd64'; }
  curl() {
    printf 'curl %s\n' "$*" >>"$log"
    printf 'test signing key\n' >"${@: -1}"
  }
  install() {
    printf 'install %s\n' "$*" >>"$log"
    if [[ "$*" == *docker.sources* ]]; then
      cp "${@: -2:1}" "$test_dir/$scenario.sources"
    fi
  }
  apt-get() {
    printf 'apt-get %s\n' "$*" >>"$log"
    if [[ "$1" == update ]]; then ! "$update_fail"; return; fi
    if $packages_fail; then return 1; fi
    tools=true
    if [[ "$*" == *docker-ce-cli* ]]; then engine=true; fi
    if [[ "$*" == *docker-compose-plugin* ]]; then compose=true; fi
  }
  dnf() {
    printf 'dnf %s\n' "$*" >>"$log"
    if [[ "$1" == -q ]]; then return 1; fi
    if $packages_fail; then return 1; fi
    tools=true
    if [[ "$*" == *docker-ce-cli* ]]; then engine=true; fi
    if [[ "$*" == *docker-compose-plugin* ]]; then compose=true; fi
  }
  systemctl() {
    printf 'systemctl %s\n' "$*" >>"$log"
    [[ "$scenario" != service-failure ]] || return 1
    daemon=true
  }
  ensure_tun_device() { "$tun"; }
  if [[ "$scenario" == confirmation-read-failure ]]; then
    unset -f confirm
    read() { return 1; }
  fi
  local actual=success
  ensure_host_dependencies >"$test_dir/$scenario.out" 2>&1 || actual=failure
  if [[ "$actual" != "$expected" ]]; then
    cat "$test_dir/$scenario.out" >&2
    printf 'FAIL dependency case %s: expected %s, got %s\n' "$scenario" "$expected" "$actual" >&2
    exit 1
  fi
  case "$scenario" in
    ready|unsupported-ready|no-tun|remote-context)
      [[ ! -s "$log" ]] ;;
    unsupported|unprivileged)
      [[ ! -s "$log" ]] ;;
    declined|confirmation-read-failure|conflicting-runtime|rpm-conflict)
      ! grep -q 'apt-get\|dnf\|install\|systemctl' "$log" ;;
    fresh)
      grep -q 'docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin' "$log"
      grep -q 'systemctl enable --now docker' "$log"
      grep -qx 'Suites: noble' "$test_dir/$scenario.sources"
      grep -qx 'URIs: https://download.docker.com/linux/ubuntu' "$test_dir/$scenario.sources" ;;
    compose-only)
      grep -q 'apt-get install -y --no-install-recommends --no-remove --no-upgrade docker-compose-plugin$' "$log"
      ! grep -q 'docker-ce-cli\|systemctl' "$log" ;;
    tools-only)
      grep -q 'iproute2 kmod' "$log"
      ! grep -q 'curl\|docker-ce\|systemctl' "$log" ;;
    package-failure|update-failure|rpm-package-failure)
      ! grep -q 'systemctl' "$log" ;;
    stopped|service-failure)
      grep -q 'systemctl enable --now docker' "$log"
      ! grep -q 'apt-get' "$log" ;;
    centos|rhel)
      grep -q "https://download.docker.com/linux/$DOCKER_REPO_DISTRO/docker-ce.repo" "$log"
      grep -q 'dnf install -y --setopt=install_weak_deps=False docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin' "$log"
      grep -q 'systemctl enable --now docker' "$log" ;;
    rpm-compose-only)
      grep -q 'dnf install -y --setopt=install_weak_deps=False docker-compose-plugin$' "$log"
      ! grep -q 'docker-ce-cli\|systemctl' "$log" ;;
  esac
  printf 'OK   dependency case %s\n' "$scenario"
)

for scenario in fresh ready compose-only tools-only stopped unsupported unsupported-ready unprivileged declined confirmation-read-failure conflicting-runtime package-failure update-failure no-tun remote-context service-failure centos rhel rpm-compose-only rpm-conflict rpm-package-failure; do
  run_case "$scenario"
done

(
  source "$repo_root/install.sh"
  have() { return 0; }
  for distro in 'ubuntu:22.04:jammy' 'ubuntu:24.04:noble' 'ubuntu:26.04:resolute' 'debian:12:bookworm' 'debian:13:trixie' 'centos:9:' 'centos:10:' 'rhel:8.10:' 'rhel:9.6:' 'rhel:10.0:'; do
    IFS=: read -r DISTRO_ID DISTRO_VERSION DISTRO_CODENAME <<<"$distro"
    dependency_bootstrap_supported
  done
  for distro in 'ubuntu:20.04:focal' 'debian:11:bullseye' 'linuxmint:22:noble' 'ubuntu:24.04:wrong' 'centos:7:' 'rhel:7.9:'; do
    IFS=: read -r DISTRO_ID DISTRO_VERSION DISTRO_CODENAME <<<"$distro"
    if dependency_bootstrap_supported; then
      printf 'FAIL unsupported distro accepted: %s\n' "$distro" >&2
      exit 1
    fi
  done
  docker() { return 1; }
  docker-compose() { return 1; }
  if compose_cmd >/dev/null; then
    printf 'FAIL broken legacy Compose accepted\n' >&2
    exit 1
  fi
  docker-compose() { return 0; }
  [[ "$(compose_cmd)" == docker-compose ]]
  printf 'OK   supported distributions and legacy Compose validation\n'
)

(
  source "$repo_root/install.sh"
  cd "$test_dir"
  selinux_volume_suffix() { printf ':Z'; }
  mkdir data
  printf 'DATABASE_MODE=off\n' >.env
  write_compose_if_missing
  grep -qx '      - ./data:/etc/awg-forge:Z' docker-compose.yml
  grep -qx '      - /lib/modules:/lib/modules:ro' docker-compose.yml
  upgrade_install_is_managed
  docker() { printf '%s\n' "$*" >>"$test_dir/selinux-docker.log"; }
  initialize_state example.com awg0 51820 eth0 10.8.0.0/24 1.1.1.1 0.0.0.0/0 25 1280 awg_2_0
  grep -q -- '-v .*/data:/etc/awg-forge:Z ' "$test_dir/selinux-docker.log"
  printf '# operator customization\n' >>docker-compose.yml
  write_compose_if_missing
  grep -qx '# operator customization' docker-compose.yml
  printf 'OK   SELinux labels private data for init and Compose and preserves custom files\n'
)

printf 'OK   dependency bootstrap scenarios passed\n'

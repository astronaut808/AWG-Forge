#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$repo_root/install.sh"

test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

require_tty() { :; }
prepare_workdir() { cd "$CURRENT_CASE"; }
have() { [[ "$1" == "docker" ]]; }
uname() { printf 'Linux'; }

confirm_answer=n
confirm() {
  printf '%s\n' "$1" >>"$DOCKER_LOG"
  [[ "$confirm_answer" == "y" ]]
}

doctor_result=ok
migration_result=ok
runtime_result=ok
docker() {
  printf '%s\n' "$*" >>"$DOCKER_LOG"
  case "$1 ${2:-} ${3:-}" in
    "compose version "|"info  ") return 0 ;;
    "inspect --format {{.Image}}") printf 'sha256:previous-image\n'; return 0 ;;
    "inspect --format {{.State.Running}}")
      if [[ "$runtime_result" == "fail" ]]; then
        printf 'false\n'
      else
        printf 'true\n'
      fi
      return 0
      ;;
    "inspect awg-forge ") return 0 ;;
    "compose config -q") return 0 ;;
    "compose pull awg-forge"|"compose stop awg-forge"|"compose start awg-forge"|"compose down --remove-orphans") return 0 ;;
    "compose run --rm")
      if [[ "$migration_result" == "fail" ]]; then
        return 1
      fi
      printf 'migrated\n' >>"$DATA_DIR/migration.log"
      return 0
      ;;
    "compose up -d") return 0 ;;
    "compose -f docker-compose.yml") return 0 ;;
    "exec awg-forge awg-forge")
      if [[ "${4:-}" == "doctor" ]]; then
        if [[ "$doctor_result" == "fail" ]]; then
          printf 'FAIL runtime: unavailable\n'
          return 0
        fi
        printf 'OK   runtime: healthy\n'
        return 0
      fi
      if [[ "${4:-}" == "db" && "${5:-}" == "status" ]]; then
        printf 'OK   database: sqlite\n'
        return 0
      fi
      ;;
  esac
  printf 'unexpected docker invocation: %s\n' "$*" >&2
  return 1
}

setup_case() {
  local name="$1"
  CURRENT_CASE="$test_dir/$name"
  mkdir -p "$CURRENT_CASE/data"
  cd "$CURRENT_CASE"
  ENV_FILE=.env
  COMPOSE_FILE=docker-compose.yml
  DATA_DIR=data
  APP_NAME=awg-forge
  DOCKER_LOG="$CURRENT_CASE/docker.log"
  : >"$DOCKER_LOG"
  cat >"$COMPOSE_FILE" <<'EOF'
services:
  awg-forge:
    image: ghcr.io/astronaut808/awg-forge:latest
    env_file: .env
    volumes:
      - ./data:/etc/awg-forge
EOF
  cat >"$ENV_FILE" <<'EOF'
WEBUI_HOST=127.0.0.1
WEBUI_PORT=51821
PASSWORD=password
SESSION_SECRET=secret
EXTERNAL_INTERFACE=eth0
DATABASE_MODE=off
DATABASE_PATH=/etc/awg-forge/awg-forge.db
EOF
  printf 'original\n' >"$DATA_DIR/sentinel"
  confirm_answer=n
  doctor_result=ok
  migration_result=ok
  runtime_result=ok
}

assert_contains() {
  local value="$1"
  local file="$2"
  if ! grep -Fqx "$value" "$file"; then
    printf 'FAIL expected %q in %s\n' "$value" "$file" >&2
    exit 1
  fi
}

assert_log_contains() {
  local value="$1"
  if ! grep -Fq "$value" "$DOCKER_LOG"; then
    printf 'FAIL expected %q in %s\n' "$value" "$DOCKER_LOG" >&2
    exit 1
  fi
}

setup_case sqlite-off
upgrade_main
assert_contains 'DATABASE_MODE=off' "$ENV_FILE"
if grep -Fq 'compose run --rm --no-deps awg-forge db migrate' "$DOCKER_LOG"; then
  printf 'FAIL disabled SQLite unexpectedly migrated\n' >&2
  exit 1
fi
assert_contains 'compose up -d --force-recreate awg-forge' "$DOCKER_LOG"
printf 'OK   upgrade keeps SQLite disabled when declined\n'

setup_case sqlite-enable
confirm_answer=y
upgrade_main
assert_contains 'DATABASE_MODE=sqlite' "$ENV_FILE"
assert_contains 'compose run --rm --no-deps awg-forge db migrate' "$DOCKER_LOG"
assert_contains 'exec awg-forge awg-forge db status' "$DOCKER_LOG"
printf 'OK   upgrade enables and migrates SQLite on confirmation\n'

setup_case sqlite-existing
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
upgrade_main
assert_contains 'compose run --rm --no-deps awg-forge db migrate' "$DOCKER_LOG"
if grep -Fq 'Enable SQLite during this upgrade?' "$DOCKER_LOG"; then
  printf 'FAIL enabled SQLite prompted again\n' >&2
  exit 1
fi
printf 'OK   upgrade migrates existing SQLite without prompting\n'

setup_case sqlite-missing
set_env_value DATABASE_MODE sqlite
if upgrade_main; then
  printf 'FAIL missing SQLite database unexpectedly succeeded without confirmation\n' >&2
  exit 1
fi
if grep -Eq '^compose (pull|stop|run|up|down|start)' "$DOCKER_LOG"; then
  printf 'FAIL missing SQLite database changed Docker state\n' >&2
  exit 1
fi
printf 'OK   missing SQLite database requires confirmation before upgrade\n'

setup_case migration-failure
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
migration_result=fail
if upgrade_main; then
  printf 'FAIL migration failure unexpectedly succeeded\n' >&2
  exit 1
fi
assert_contains 'compose start awg-forge' "$DOCKER_LOG"
if grep -Fq 'compose up -d --force-recreate awg-forge' "$DOCKER_LOG"; then
  printf 'FAIL migration failure started the target container\n' >&2
  exit 1
fi
printf 'OK   migration failure restores the stopped container\n'

setup_case doctor-failure
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
doctor_result=fail
if ! upgrade_main; then
  printf 'FAIL doctor warning unexpectedly rolled back the upgrade\n' >&2
  exit 1
fi
if grep -Fq 'compose down --remove-orphans' "$DOCKER_LOG"; then
  printf 'FAIL doctor warning rolled back the upgraded container\n' >&2
  exit 1
fi
printf 'OK   Doctor warning does not roll back a running service\n'

setup_case startup-failure
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
runtime_result=fail
if upgrade_main; then
  printf 'FAIL stopped target container unexpectedly succeeded\n' >&2
  exit 1
fi
assert_contains 'compose down --remove-orphans' "$DOCKER_LOG"
assert_log_contains 'compose -f docker-compose.yml -f '
assert_contains 'original' "$DATA_DIR/sentinel"
printf 'OK   startup failure restores backup and previous image\n'

setup_case custom-layout
sed -i.bak 's|./data:/etc/awg-forge|./other:/etc/awg-forge|' "$COMPOSE_FILE"
if upgrade_main; then
  printf 'FAIL custom layout unexpectedly succeeded\n' >&2
  exit 1
fi
if grep -Eq '^compose (pull|stop|run|up|down|start)' "$DOCKER_LOG"; then
  printf 'FAIL custom layout changed Docker state\n' >&2
  exit 1
fi
printf 'OK   custom layout is rejected before Docker changes\n'

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
timeout() { shift 3; "$@"; }
sleep() { SECONDS=$((SECONDS + 2)); }

if ! command -v flock >/dev/null; then
  # Match Linux flock semantics on the inherited directory descriptor in macOS tests.
  flock() {
    python3 -c 'import fcntl, sys; fcntl.flock(int(sys.argv[1]), fcntl.LOCK_EX | fcntl.LOCK_NB)' "$2" 2>/dev/null
  }
fi

findmnt() {
  jq -n --arg target "${host_mount:-/}" '{filesystems: [{target: $target}]}'
}

confirm() {
  printf '%s\n' "$1" >>"$DOCKER_LOG"
  [[ "$confirm_answer" == "y" ]]
}

cp() {
  if [[ "$copy_failure" == backup && "${2:-}" == "$DATA_DIR" && "${3:-}" == upgrade-backup-* ]] ||
     [[ "$copy_failure" == restore && "${2:-}" == upgrade-backup-*/"$DATA_DIR" && "${3:-}" == .upgrade-restore.* ]]; then
    return 1
  fi
  command cp "$@"
}

fixture_env() {
  jq -n --arg mode "$(env_value DATABASE_MODE)" --arg password "$(env_value PASSWORD)" \
    --arg config "$1" --arg db "$2" \
    '{DATABASE_MODE:$mode,PASSWORD:$password,CONFIG_DIR:$config,DATABASE_PATH:$db}'
}

curl() {
  printf 'probe %s\n' "${*: -1}" >>"$DOCKER_LOG"
  printf 'probe-options %s\n' "$*" >>"$DOCKER_LOG"
  if [[ "$runtime_result" == timeout && "$(<.fixture-image)" == "$NEW_IMAGE" ]]; then
    printf 503
  else
    printf 401
  fi
}

docker() {
  printf '%s\n' "$*" >>"$DOCKER_LOG"
  local override="" phase image
  if [[ "$1" == compose && "${2:-}" == -f && "${3:-}" != - ]]; then
    override="$5"
    shift 5
    set -- compose "$@"
  fi
  case "$1 ${2:-} ${3:-}" in
    "compose version "|"info  ") return 0 ;;
    "compose -f -")
      cat >/dev/null
      [[ "$compose_no_interpolate" == true ]] || return 1
      if [[ "$compose_env_file_shape" == missing-metadata ]]; then
        printf '{"services":{"awg-forge":{}}}\n'
      else
        printf '{"services":{"awg-forge":{"env_file":[{"path":"/dev/null"}]}}}\n'
      fi
      ;;
    "context inspect --format") printf 'unix:///var/run/docker.sock\n' ;;
    "inspect --format {{.Id}}") printf 'fixture-container\n' ;;
    "inspect --format {{index .Config.Labels"*)
      printf '%s/docker-compose.yml' "$(pwd -P)"
      case "$compose_override" in
        custom) printf ',%s/docker-compose.override.yml' "$(pwd -P)" ;;
        rollback) printf ',%s/.awg-forge-rollback.Ab12Cd' "$(pwd -P)" ;;
        upgrade) printf ',%s/.awg-forge-upgrade.Ab12Cd' "$(pwd -P)" ;;
      esac
      printf '\n'
      ;;
    "inspect --format {{json .Mounts}}")
      jq -n --arg path "$(pwd -P)/data" --arg extra "$existing_mount" '
        [{Type:"bind",Source:$path,Destination:"/etc/awg-forge",RW:true}] +
        (if $extra == "" then [] else [{Type:"volume",Destination:$extra,RW:true}] end)'
      ;;
    "inspect --format {{.HostConfig.NetworkMode}}:{{len .HostConfig.Tmpfs}}") printf 'host:0\n' ;;
    "inspect fixture-container --format"|"inspect --format {{json .Config.Env}}")
      fixture_env "$existing_config" "" | jq '[to_entries[] | .key + "=" + .value]'
      ;;
    "image inspect --format")
      case "$4" in
        '{{.Id}}') printf '%s\n' "$NEW_IMAGE" ;;
        '{{json .Config.Volumes}}') printf '{"/etc/awg-forge":{}}\n' ;;
      esac
      ;;
    "image inspect sha256:"*) printf '[]\n' ;;
    "compose ps -aq") printf '%s\n' "$compose_container" ;;
    "inspect --format {{.Image}}") printf '%s\n' "$(<.fixture-image)" ;;
    "inspect --format {{.State.Running}}")
      [[ "$stop_inspect_failure" != true ]] || return 1
      printf 'false\n'
      ;;
    "inspect --format {{.Image}} {{.State.Status}} {{.RestartCount}} {{.State.StartedAt}}")
      image=$(<.fixture-image)
      phase=running
      if [[ "$image" == "$NEW_IMAGE" ]]; then
        if [[ "$runtime_result" == fail ]]; then
          phase=exited
        elif [[ "$runtime_result" == late ]] && grep -Fq 'probe ' "$DOCKER_LOG"; then
          phase=exited
        fi
      fi
      printf '%s %s 0 fixture-timestamp\n' "$image" "$phase"
      ;;
    "compose config --no-env-resolution"|"compose config --no-interpolate"|"compose config --format")
      local data_path image_ref=ghcr.io/astronaut808/awg-forge:latest
      [[ -z "$override" ]] || image_ref="$(awk '/image:/{print $2}' "$override")"
      data_path="$(pwd -P)/data"
      [[ "$target_layout" != wrong ]] || data_path="$(pwd -P)/other"
      fixture_env "$target_config" "$target_db" | jq --arg dir "$PWD" --arg data "$data_path" --arg extra "$target_mount" --arg option "$3" --arg image "$image_ref" --arg shape "$compose_env_file_shape" '
        ($dir + "/.env") as $env |
        {services: {"awg-forge": ({
          image: $image, container_name: "awg-forge", network_mode: "host",
          volumes: ([{type:"bind",source:$data,target:"/etc/awg-forge"}] +
            if $extra == "" then [] else [{type:"volume",target:$extra}] end)
        } + (if $option == "--no-interpolate" then
          {env_file: (if $shape == "string" then [$env]
            elif $shape == "wrong" then [$env + ".override"]
            elif $shape == "interpolated" then ["${AWG_ENV_FILE:-.env}"]
            else [{path:$env}] end)} +
          (if $shape == "env-list" then {environment:["LOG_LEVEL=info"]}
            elif $shape == "db-override-list" then {environment:["DATABASE_MODE=off"]}
            else {} end)
          elif $option == "--no-env-resolution" and $shape == "interpolated" then {env_file:[{path:$env}]}
          elif $option == "--no-env-resolution" and $shape == "wrong-resolved" then {env_file:[$env + ".override"]}
          elif $option == "--no-env-resolution" and $shape == "db-dependent" then
            {env_file: [if .DATABASE_MODE == "sqlite" then $env + ".override" else $env end]}
          else {environment:.} end))}}'
      ;;
    "compose pull awg-forge")
      printf 'pulled-image %s\n' "$(awk '/image:/{print $2}' "$override")" >>"$DOCKER_LOG"
      [[ "$pull_result" != fail ]] || return 1
      if [[ "$wait_in_pull" == true ]]; then
        touch .fixture-pulling
        for _ in {1..250}; do
          [[ ! -e .fixture-continue ]] || return 0
          command sleep 0.02
        done
        return 1
      fi
      ;;
    "compose stop awg-forge")
      if [[ "$rollback_stop_failure" == true && "$(<.fixture-image)" == "$NEW_IMAGE" ]]; then
        return 1
      fi
      ;;
    "compose start awg-forge") return 0 ;;
    "compose run --rm")
      [[ "$(awk '/image:/{print $2}' "$override")" == "$NEW_IMAGE" ]] || return 1
      if [[ "$migration_result" == "fail" ]]; then
        printf 'modified during migration\n' >"$DATA_DIR/sentinel"
        return 1
      fi
      touch "$DATA_DIR/awg-forge.db"
      printf 'migrated\n' >>"$DATA_DIR/migration.log"
      printf 'OK   database: sqlite\nOK   schema: 4\n'
      ;;
    "compose up -d")
      image="$(awk '/image:/{print $2}' "$override")"
      printf '%s\n' "$image" >.fixture-image
      if [[ "$image" == "$NEW_IMAGE" ]]; then
        printf 'replacement wrote data\n' >"$DATA_DIR/sentinel"
      fi
      ;;
    "exec awg-forge awg-forge")
      if [[ "${4:-}" == "doctor" ]]; then
        if [[ "$doctor_result" == "fail" ]]; then
          printf 'FAIL runtime: unavailable\n'
        else
          printf 'OK   runtime: healthy\n'
        fi
        return 0
      fi
      return 1
      ;;
    "exec fixture-container awg-forge")
      [[ "${4:-} ${5:-}" == "db status" ]] || return 1
      printf 'OK   database: %s\nOK   schema: %s\n' "$fixture_database_status" "$fixture_schema"
      ;;
    *)
      printf 'unexpected docker invocation: %s\n' "$*" >&2
      return 1
      ;;
  esac
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
  UPGRADE_IMAGE=""
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
  printf '{}\n' >"$DATA_DIR/state.json"
  OLD_IMAGE="$(printf 'sha256:%064d' 1)"
  NEW_IMAGE="$(printf 'sha256:%064d' 2)"
  printf '%s\n' "$OLD_IMAGE" >.fixture-image
  confirm_answer=n
  host_mount=""
  existing_mount=""
  existing_config=""
  target_mount=""
  target_layout=managed
  target_config=""
  target_db=""
  wait_in_pull=false
  pull_result=ok
  rollback_stop_failure=false
  stop_inspect_failure=false
  fixture_schema=4
  doctor_result=ok
  migration_result=ok
  runtime_result=ok
  copy_failure=none
  compose_container=fixture-container
  compose_override=none
  compose_no_interpolate=true
  compose_env_file_shape=object
  fixture_database_status=sqlite
}

assert_contains() {
  local value="$1"
  local file="$2"
  if ! grep -Fqx -- "$value" "$file"; then
    printf 'FAIL expected %q in %s\n' "$value" "$file" >&2
    exit 1
  fi
}

assert_log_contains() {
  local value="$1"
  if ! grep -Fq -- "$value" "$DOCKER_LOG"; then
    printf 'FAIL expected %q in %s\n' "$value" "$DOCKER_LOG" >&2
    exit 1
  fi
}

setup_case sqlite-off
upgrade_main
assert_contains 'pulled-image ghcr.io/astronaut808/awg-forge:latest' "$DOCKER_LOG"
assert_contains 'DATABASE_MODE=off' "$ENV_FILE"
if grep -Fq 'run --rm --no-deps --pull never -T awg-forge db migrate' "$DOCKER_LOG"; then
  printf 'FAIL disabled SQLite unexpectedly migrated\n' >&2
  exit 1
fi
assert_log_contains 'up -d --no-deps --force-recreate --pull never --no-build awg-forge'
printf 'OK   upgrade keeps SQLite disabled when declined\n'

setup_case compose-string-env-file
compose_env_file_shape=string
upgrade_main
printf 'OK   upgrade accepts Compose string env_file paths\n'

setup_case compose-interpolated-env-file
compose_env_file_shape=interpolated
upgrade_main
printf 'OK   upgrade accepts a resolved managed env_file path\n'

setup_case compose-wrong-resolved-env-file
compose_env_file_shape=wrong-resolved
if upgrade_main >upgrade.log 2>&1; then
  printf 'FAIL upgrade accepted the raw path after rejecting the resolved env_file\n' >&2
  exit 1
fi
grep -Fq 'cannot be restored safely' upgrade.log
printf 'OK   upgrade refuses a resolved path outside the managed env_file\n'

setup_case compose-env-file-changes-after-sqlite
compose_env_file_shape=db-dependent
confirm_answer=y
if upgrade_main >upgrade.log 2>&1; then
  printf 'FAIL upgrade accepted a changed env_file after enabling SQLite\n' >&2
  exit 1
fi
grep -Fq 'could not update SQLite settings with a safe Compose layout' upgrade.log
assert_contains 'DATABASE_MODE=off' "$ENV_FILE"
assert_contains "$OLD_IMAGE" .fixture-image
assert_log_contains 'compose start awg-forge'
if grep -Eq ' (run --rm|up -d) ' "$DOCKER_LOG"; then
  printf 'FAIL upgrade used the changed Compose layout\n' >&2
  exit 1
fi
[[ ! -e .awg-forge-upgrade-pending ]]
printf 'OK   upgrade rolls back when enabling SQLite changes the resolved env_file\n'

setup_case compose-symlink-workdir
ln -s "$CURRENT_CASE" "$test_dir/linked-installation"
cd "$test_dir/linked-installation"
upgrade_compose_layout "$(pwd -P)" "$(pwd -P)/data" docker compose
printf 'OK   Compose validation uses the physical installation directory\n'

setup_case compose-list-environment
compose_env_file_shape=env-list
upgrade_main
printf 'OK   upgrade accepts Compose environment lists with safe keys\n'

setup_case compose-list-database-override
compose_env_file_shape=db-override-list
if upgrade_main >upgrade.log 2>&1; then
  printf 'FAIL upgrade accepted a DATABASE_MODE override in an environment list\n' >&2
  exit 1
fi
grep -Fq 'cannot be restored safely' upgrade.log
printf 'OK   upgrade rejects DATABASE_MODE overrides in Compose environment lists\n'

setup_case compose-wrong-env-file
compose_env_file_shape=wrong
if upgrade_main >upgrade.log 2>&1; then
  printf 'FAIL upgrade accepted an unexpected env_file path\n' >&2
  exit 1
fi
grep -Fq 'cannot be restored safely' upgrade.log
if grep -Eq '^compose (pull|stop) ' "$DOCKER_LOG"; then
  printf 'FAIL env_file validation failure changed Docker state\n' >&2
  exit 1
fi
printf 'OK   upgrade rejects an unexpected Compose env_file path\n'

setup_case compose-no-interpolate
compose_no_interpolate=false
if upgrade_main >upgrade.log 2>&1; then
  printf 'FAIL upgrade accepted unsupported Compose\n' >&2
  exit 1
fi
grep -Fq 'cannot report env_file in its config model; update Docker Compose' upgrade.log
if grep -Eq '^compose (pull|stop) ' "$DOCKER_LOG"; then
  printf 'FAIL unsupported Compose changed Docker state\n' >&2
  exit 1
fi
printf 'OK   upgrade reports unsupported Compose before pulling or stopping\n'

setup_case compose-missing-env-metadata
compose_env_file_shape=missing-metadata
if upgrade_main >upgrade.log 2>&1; then
  printf 'FAIL upgrade accepted a Compose model without env_file metadata\n' >&2
  exit 1
fi
grep -Fq 'cannot report env_file in its config model; update Docker Compose' upgrade.log
if grep -Eq '^compose (pull|stop) ' "$DOCKER_LOG"; then
  printf 'FAIL incomplete Compose model changed Docker state\n' >&2
  exit 1
fi
printf 'OK   upgrade diagnoses Compose versions that silently omit env_file\n'

setup_case explicit-image
UPGRADE_IMAGE=ghcr.io/astronaut808/awg-forge:rc-fixture
cp "$COMPOSE_FILE" compose-before
upgrade_main
assert_contains "pulled-image $UPGRADE_IMAGE" "$DOCKER_LOG"
assert_contains "image inspect --format {{.Id}} $UPGRADE_IMAGE" "$DOCKER_LOG"
cmp compose-before "$COMPOSE_FILE"
[[ ! -e .awg-forge-upgrade-pending ]]
printf 'OK   IMAGE selects the inspected and pinned target without rewriting base Compose\n'

setup_case invalid-image
UPGRADE_IMAGE=$'ghcr.io/astronaut808/awg-forge:rc\nservices:'
if upgrade_main; then
  printf 'FAIL invalid IMAGE unexpectedly upgraded\n' >&2
  exit 1
fi
if grep -Eq '^pulled-image |^compose (stop|start)| (run --rm|up -d) ' "$DOCKER_LOG"; then
  printf 'FAIL invalid IMAGE changed Docker state\n' >&2
  exit 1
fi
printf 'OK   invalid IMAGE is rejected before pulling or stopping\n'

setup_case explicit-image-pull-failure
UPGRADE_IMAGE=ghcr.io/astronaut808/awg-forge:rc-fixture
pull_result=fail
if upgrade_main; then
  printf 'FAIL failed IMAGE pull unexpectedly upgraded\n' >&2
  exit 1
fi
assert_contains 'original' "$DATA_DIR/sentinel"
if grep -Eq '^compose (stop|start)| (run --rm|up -d) ' "$DOCKER_LOG"; then
  printf 'FAIL failed IMAGE pull stopped or recreated the service\n' >&2
  exit 1
fi
printf 'OK   failed IMAGE pull preserves the running installation\n'

setup_case explicit-image-rollback
UPGRADE_IMAGE=ghcr.io/astronaut808/awg-forge:rc-fixture
runtime_result=late
cp "$COMPOSE_FILE" compose-before
if upgrade_main; then
  printf 'FAIL failed IMAGE startup unexpectedly upgraded\n' >&2
  exit 1
fi
assert_contains "$OLD_IMAGE" .fixture-image
assert_contains 'original' "$DATA_DIR/sentinel"
assert_contains 'PASSWORD=password' "$ENV_FILE"
cmp compose-before "$COMPOSE_FILE"
[[ ! -e .awg-forge-upgrade-pending ]]
printf 'OK   failed IMAGE startup restores the actual previous image and base configuration\n'

setup_case sqlite-enable
confirm_answer=y
upgrade_main
assert_contains 'DATABASE_MODE=sqlite' "$ENV_FILE"
assert_log_contains 'run --rm --no-deps --pull never -T awg-forge db migrate'
assert_contains 'exec fixture-container awg-forge db status' "$DOCKER_LOG"
printf 'OK   upgrade enables and migrates SQLite on confirmation\n'

setup_case sqlite-existing
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
upgrade_main
assert_log_contains 'run --rm --no-deps --pull never -T awg-forge db migrate'
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
if grep -Eq '^compose (pull|stop|run|up|down|start)|^compose .* (pull|run|up) ' "$DOCKER_LOG"; then
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
if grep -Eq ' up -d ' "$DOCKER_LOG"; then
  printf 'FAIL migration failure started the target container\n' >&2
  exit 1
fi
assert_contains 'original' "$DATA_DIR/sentinel"
printf 'OK   migration failure restores the stopped container\n'

setup_case backup-copy-failure
copy_failure=backup
if upgrade_main; then
  printf 'FAIL incomplete backup unexpectedly allowed an upgrade\n' >&2
  exit 1
fi
assert_contains 'compose start awg-forge' "$DOCKER_LOG"
assert_contains 'original' "$DATA_DIR/sentinel"
if grep -Eq ' (run --rm|up -d) ' "$DOCKER_LOG"; then
  printf 'FAIL incomplete backup allowed migration or recreation\n' >&2
  exit 1
fi
printf 'OK   incomplete backup prevents migration and restarts the old container\n'

setup_case stopped-check-failure
stop_inspect_failure=true
if upgrade_main; then
  printf 'FAIL failed stop verification unexpectedly allowed an upgrade\n' >&2
  exit 1
fi
assert_contains 'compose start awg-forge' "$DOCKER_LOG"
assert_contains 'original' "$DATA_DIR/sentinel"
[[ ! -e .awg-forge-upgrade-pending ]]
if grep -Eq ' (run --rm|up -d) ' "$DOCKER_LOG"; then
  printf 'FAIL failed stop verification allowed migration or recreation\n' >&2
  exit 1
fi
printf 'OK   failed stop verification restarts the old container\n'

setup_case restore-copy-failure
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
migration_result=fail
copy_failure=restore
if upgrade_main; then
  printf 'FAIL incomplete restore unexpectedly succeeded\n' >&2
  exit 1
fi
assert_contains 'modified during migration' "$DATA_DIR/sentinel"
if grep -Eq ' (start awg-forge|up -d)($| )' "$DOCKER_LOG"; then
  printf 'FAIL incomplete restore restarted a container\n' >&2
  exit 1
fi
printf 'OK   restore copy failure retains data and leaves the service stopped\n'

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
[[ "$(grep -Fxc 'compose stop awg-forge' "$DOCKER_LOG")" == 2 ]]
assert_log_contains 'compose -f docker-compose.yml -f '
assert_log_contains 'up -d --no-deps --force-recreate --pull never --no-build awg-forge'
assert_contains 'original' "$DATA_DIR/sentinel"
printf 'OK   startup failure restores backup and previous image\n'

setup_case database-disabled
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
fixture_database_status=disabled
if upgrade_main; then
  printf 'FAIL SQLite verification accepted a disabled database\n' >&2
  exit 1
fi
assert_log_contains 'compose -f docker-compose.yml -f '
printf 'OK   SQLite status mismatch restores the previous image\n'

setup_case different-compose-container
compose_container=other-container
if upgrade_main; then
  printf 'FAIL mismatched Compose container unexpectedly upgraded\n' >&2
  exit 1
fi
if grep -Eq '^compose (pull|stop|run|up|start)|^compose .* (pull|run|up) ' "$DOCKER_LOG"; then
  printf 'FAIL mismatched Compose container changed Docker state\n' >&2
  exit 1
fi
printf 'OK   mismatched Compose container is rejected before Docker changes\n'

setup_case compose-override
compose_override=custom
if upgrade_main; then
  printf 'FAIL Compose override unexpectedly allowed a managed upgrade\n' >&2
  exit 1
fi
if grep -Eq '^compose (pull|stop|run|up|start)|^compose .* (pull|run|up) ' "$DOCKER_LOG"; then
  printf 'FAIL Compose override changed Docker state\n' >&2
  exit 1
fi
printf 'OK   Compose override is rejected before Docker changes\n'

setup_case retry-after-rollback
compose_override=rollback
upgrade_main
assert_log_contains 'up -d --no-deps --force-recreate --pull never --no-build awg-forge'
printf 'OK   a new upgrade is allowed after restoring the previous image\n'

setup_case custom-layout
target_layout=wrong
if upgrade_main; then
  printf 'FAIL custom layout unexpectedly succeeded\n' >&2
  exit 1
fi
if grep -Eq '^compose (pull|stop|run|up|down|start)|^compose .* (pull|run|up) ' "$DOCKER_LOG"; then
  printf 'FAIL custom layout changed Docker state\n' >&2
  exit 1
fi
printf 'OK   custom layout is rejected before Docker changes\n'

assert_preflight_rejects() {
  local output
  if output=$(upgrade_main 2>&1); then
    printf 'FAIL unsafe layout unexpectedly upgraded\n' >&2
    exit 1
  fi
  if [[ "$output" == *"sensitive-fixture-value"* ]] ||
     grep -Eq '^compose (pull|stop|run|up|down|start)|^compose .* (pull|run|up) |^compose .* (pull|run|up) ' "$DOCKER_LOG"; then
    printf 'FAIL preflight printed a secret or changed Docker state\n' >&2
    exit 1
  fi
}

setup_case nested-container-mount
existing_mount=/etc/awg-forge/tunnels
assert_preflight_rejects

setup_case nested-compose-mount
target_mount=/etc/awg-forge/tls
assert_preflight_rejects

setup_case mounted-host-data
host_mount="$(pwd -P)/data"
assert_preflight_rejects

setup_case mounted-child-path
host_mount="$(pwd -P)/data/tls path"
assert_preflight_rejects

setup_case linked-data
ln -s ../.env "$DATA_DIR/linked"
assert_preflight_rejects

setup_case external-existing-config
existing_config=/external/config
assert_preflight_rejects

setup_case external-target-database
set_env_value PASSWORD sensitive-fixture-value
target_db=/external/awg-forge.db
assert_preflight_rejects
printf 'OK   upgrade refuses unsafe mounts, links and effective environment without disclosing secrets\n'

setup_case late-failure
confirm_answer=y
runtime_result=late
if upgrade_main; then
  printf 'FAIL late startup exit unexpectedly succeeded\n' >&2
  exit 1
fi
assert_log_contains 'probe http://127.0.0.1:51821/api/state'
assert_contains "$OLD_IMAGE" .fixture-image
assert_contains 'original' "$DATA_DIR/sentinel"
assert_contains 'DATABASE_MODE=off' "$ENV_FILE"
assert_contains 'PASSWORD=password' "$ENV_FILE"
[[ ! -e .awg-forge-upgrade-pending ]]
runtime_result=ok
compose_override=upgrade
upgrade_main
assert_contains "$NEW_IMAGE" .fixture-image
printf 'OK   upgrade rolls back a late exit and can retry after recovery\n'

setup_case readiness-timeout
runtime_result=timeout
if upgrade_main; then
  printf 'FAIL unavailable HTTP service unexpectedly upgraded\n' >&2
  exit 1
fi
assert_contains "$OLD_IMAGE" .fixture-image
assert_contains 'original' "$DATA_DIR/sentinel"
printf 'OK   upgrade rolls back when HTTP readiness times out\n'

setup_case tls-readiness
mkdir "$DATA_DIR/tls"
printf '{"mode":"manual"}\n' >"$DATA_DIR/tls/config.json"
upgrade_main
assert_log_contains 'probe https://127.0.0.1:51821/api/state'
printf 'OK   upgrade probes HTTPS for a TLS installation\n'

setup_case acme-domain-readiness
mkdir "$DATA_DIR/tls"
printf '{"mode":"acme-domain","acme_domain":"panel.example.test"}\n' >"$DATA_DIR/tls/config.json"
upgrade_main
assert_log_contains 'probe https://panel.example.test:51821/api/state'
assert_log_contains '--connect-to panel.example.test:51821:127.0.0.1:51821'
if grep -E '^probe-options .*--insecure' "$DOCKER_LOG"; then
  printf 'FAIL public ACME certificate verification was disabled\n' >&2
  exit 1
fi
printf 'OK   upgrade preserves domain SNI and certificate verification in ACME readiness\n'

setup_case acme-ip-readiness
mkdir "$DATA_DIR/tls"
printf '{"mode":"acme-ip","acme_ip":"203.0.113.10"}\n' >"$DATA_DIR/tls/config.json"
upgrade_main
assert_log_contains 'probe https://203.0.113.10:51821/api/state'
assert_log_contains '--connect-to 203.0.113.10:51821:127.0.0.1:51821'
printf 'OK   upgrade preserves the ACME IP certificate identity in readiness\n'

setup_case reverse-proxy-readiness
mkdir "$DATA_DIR/tls"
printf '{"mode":"reverse-proxy"}\n' >"$DATA_DIR/tls/config.json"
upgrade_main
assert_log_contains 'probe http://127.0.0.1:51821/api/state'
printf 'OK   upgrade probes the local HTTP listener behind a TLS proxy\n'

setup_case sqlite-schema-mismatch
set_env_value DATABASE_MODE sqlite
touch "$DATA_DIR/awg-forge.db"
fixture_schema=3
if upgrade_main; then
  printf 'FAIL stale schema unexpectedly passed readiness\n' >&2
  exit 1
fi
assert_contains "$OLD_IMAGE" .fixture-image
printf 'OK   upgrade rolls back when the SQLite schema differs from the migrated schema\n'

setup_case rollback-failed
runtime_result=late
rollback_stop_failure=true
if upgrade_main; then
  printf 'FAIL upgrade with a failed rollback returned success\n' >&2
  exit 1
fi
backup_dir=$(<.awg-forge-upgrade-pending)
[[ "$(<"$backup_dir/previous-image-id")" == "$OLD_IMAGE" ]]
assert_contains 'original' "$backup_dir/data/sentinel"
assert_contains 'replacement wrote data' "$DATA_DIR/sentinel"
if upgrade_main; then
  printf 'FAIL automatic retry after failed rollback returned success\n' >&2
  exit 1
fi
[[ "$(grep -c '^pulled-image ' "$DOCKER_LOG")" == 1 ]]
printf 'OK   upgrade retains backup and blocks retry after failed rollback\n'

setup_case parallel-upgrade
wait_in_pull=true
upgrade_main >first-upgrade.log 2>&1 &
first_pid=$!
for _ in {1..250}; do
  [[ ! -e .fixture-pulling ]] || break
  command sleep 0.02
done
[[ -e .fixture-pulling ]]
if upgrade_main >second-upgrade.log 2>&1; then
  printf 'FAIL overlapping upgrade unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq 'could not lock this installation' second-upgrade.log
touch .fixture-continue
wait "$first_pid"
[[ "$(grep -c '^pulled-image ' "$DOCKER_LOG")" == 1 ]]
assert_contains "$NEW_IMAGE" .fixture-image
printf 'OK   upgrade regressions passed\n'

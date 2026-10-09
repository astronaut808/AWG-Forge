#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$repo_root/install.sh"
test_dir="$(mktemp -d)"
test_dir="$(cd "$test_dir" && pwd -P)"
trap 'rm -rf "$test_dir"' EXIT
script_sha="$(sha256sum "$repo_root/install.sh" | awk '{print $1}')"
image="sha256:$(printf '%064d' 0)"
commit="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
pin="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
fixture_container_id="dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
fixture_helper_id="eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
common=(--image "$image" --script-sha256 "$script_sha" --artifact-version local-aaaaaaaaaaaa --artifact-commit "$commit" --controller-url https://controller.example:443 --ca-pin "$pin" --invitation-id 11111111-1111-4111-8111-111111111111 --name node-a --secret-fd 0)
docker_log="$test_dir/docker.log"
docker() {
  printf '%s\n' "$*" >>"$docker_log"
  case "$1" in
    info) printf '%s\n' "${fixture_security_options:-name=seccomp,profile=builtin}" ;;
    rm|stop|ps) return 0 ;;
    image) printf '%s\n' "$image" ;;
    inspect)
      case "$3" in
        *working_dir*) printf '%s\n' "${owned_workdir:-/foreign}" ;;
        *compose.service*) printf '%s\n' "${fixture_service:-awg-forge}" ;;
        *'{{.Image}}'*) printf '%s\n' "$image" ;;
        *'{{.Id}}'*) printf '%s\n' "$fixture_container_id" ;;
        *State.Running*) printf '%s\n' "${fixture_running:-true}" ;;
        *UsernsMode*) printf '%s\n' "${fixture_userns:-}" ;;
        *Config.User*) printf '%s\n' "${fixture_user:-}" ;;
        *ProcessLabel*) printf '%s\n' "${fixture_process_label:-}" ;;
        *MountLabel*) printf '%s\n' "${fixture_mount_label:-}" ;;
        *Config.Env*) printf '%s\n' "${fixture_env:-KEEP_CUSTOM=value}" ;;
      esac ;;
    create|run)
      if [[ "$*" == *' node installer-check '* ]]; then
        [[ "${fail_metadata:-false}" != true ]] || return 1
        printf '%s\n' 'installer-onboarding-v1 compatible'
        return 0
      fi
      local previous='' arg
      for arg in "$@"; do
        [[ "$previous" != --cidfile ]] || printf '%s' "$fixture_helper_id" >"$arg"
        previous="$arg"
      done
      ;;
    start)
      if [[ "${2:-}" == -ai ]]; then
        [[ "${signal_helper:-false}" != true ]] || { kill -TERM "$fixture_installer_pid"; return 143; }
        [[ "${fail_helper:-false}" != true ]] || return 1
      else
        [[ "${fail_start:-false}" != true ]] || return 1
      fi ;;
    compose) [[ "${fail_start:-false}" != true ]] || return 1 ;;
  esac
}
timeout() {
  [[ "$1" != --foreground ]] || shift
  [[ "$1" != -k ]] || shift 2
  shift
  "$@"
}
join_require_prerequisites() { :; }
run_join() {
  (
    # $$ is inherited by Bash subshells. An exec'd child reports this exact
    # installer's PID via PPID, including on Bash 3.2 without BASHPID.
    local fixture_installer_pid
    fixture_installer_pid="$(exec "$BASH" -c 'printf "%s\n" "$PPID"')"
    main "$@"
  )
}

join_require_unlabelled_docker
if (fixture_security_options=$'name=seccomp,profile=builtin\nname=selinux'; join_require_unlabelled_docker); then
  printf 'FAIL SELinux daemon accepted for join/rebind\n' >&2; exit 1
fi
if (docker() { return 1; }; join_require_unlabelled_docker); then
  printf 'FAIL unavailable Docker security metadata accepted\n' >&2; exit 1
fi
printf 'OK SELinux daemon and unavailable security metadata deny before fresh files or service stop\n'

: >"$docker_log"
if (
  fixture_security_options=name=selinux
  join_require_prerequisites() { join_require_unlabelled_docker; }
  run_join join --workdir "$test_dir/selinux-fresh" --mode fresh "${common[@]}" </dev/null
); then
  printf 'FAIL SELinux fresh join accepted\n' >&2; exit 1
fi
[[ ! -e "$test_dir/selinux-fresh" ]]
! grep -Eq '^(stop|start|create|rm|run) ' "$docker_log"

run_join join --workdir "$test_dir/fresh" --mode fresh "${common[@]}" </dev/null
[[ -d "$test_dir/fresh/data" ]] && grep -qx DATABASE_MODE=off "$test_dir/fresh/.env"
! grep -Eq 'TUNNEL_NAME|LISTEN_PORT|WARP|ACME' "$test_dir/fresh/.env"
! grep -Eq ' node (init|join)|cleanup|down ' "$docker_log"
grep -Fqx "    image: $image" "$test_dir/fresh/docker-compose.yml"
grep -q ' node enroll .*--secret-fd 0' "$docker_log"
printf 'OK fresh explicit run_join join bypasses Init, with private DB-off state and pinned image\n'

owned_workdir="$test_dir/existing"
mkdir -p "$owned_workdir/data"
printf 'custom Compose mounts networks logging\n' >"$owned_workdir/docker-compose.yml"
printf 'CUSTOM=value\n' >"$owned_workdir/.env"
printf 'existing state bytes\n' >"$owned_workdir/data/state.json"
before="$(cksum "$owned_workdir/docker-compose.yml" "$owned_workdir/.env" "$owned_workdir/data/state.json")"
fixture_env=$'KEEP_CUSTOM=value\n'
: >"$docker_log"
run_join join --workdir "$owned_workdir" --mode existing --maintenance "${common[@]}" </dev/null
[[ "$before" == "$(cksum "$owned_workdir/docker-compose.yml" "$owned_workdir/.env" "$owned_workdir/data/state.json")" ]]
grep -qx "stop -t 30 $fixture_container_id" "$docker_log"
grep -qx "start $fixture_container_id" "$docker_log"
grep -q -- "--volumes-from $fixture_container_id" "$docker_log"
! grep -Eq '^compose | down |init|recreate' "$docker_log"
printf 'OK existing join preserves configuration and restarts only exact owned ID\n'

for failure in metadata foreign consent user userns userns_host process_label mount_label; do
  : >"$docker_log"
  if (
    case "$failure" in metadata) fail_metadata=true ;; foreign) fixture_service=foreign ;; user) fixture_user=1000 ;; userns) fixture_userns=private ;; userns_host) fixture_userns=host ;; process_label) fixture_process_label=system_u:system_r:container_t:s0:c123,c456 ;; mount_label) fixture_mount_label=system_u:object_r:container_file_t:s0:c123,c456 ;; esac
    if [[ "$failure" == consent ]]; then
      run_join join --workdir "$owned_workdir" "${common[@]}" </dev/null
    else
      run_join join --workdir "$owned_workdir" --maintenance "${common[@]}" </dev/null
    fi
  ); then printf 'FAIL expected pre-stop denial %s\n' "$failure" >&2; exit 1; fi
  ! grep -Eq '^(stop|start|rm) ' "$docker_log"
done
printf 'OK artifact, foreign ownership and missing maintenance consent deny before stop\n'

: >"$docker_log"
if ( fail_helper=true; run_join join --workdir "$owned_workdir" --maintenance "${common[@]}" </dev/null ); then exit 1; fi
grep -qx "rm -f $fixture_helper_id" "$docker_log"
grep -qx "start $fixture_container_id" "$docker_log"
[[ "$before" == "$(cksum "$owned_workdir/docker-compose.yml" "$owned_workdir/.env" "$owned_workdir/data/state.json")" ]]
printf 'OK precommit helper failure cleans exact helper and resumes previous owned service\n'

: >"$docker_log"
if ( signal_helper=true; run_join join --workdir "$owned_workdir" --maintenance "${common[@]}" </dev/null ); then exit 1; else result=$?; fi
[[ "$result" == 143 ]] || { printf 'FAIL installer SIGTERM exit: expected 143, got %s\n' "$result" >&2; exit 1; }
grep -qx "rm -f $fixture_helper_id" "$docker_log"
grep -qx "start $fixture_container_id" "$docker_log"
[[ "$before" == "$(cksum "$owned_workdir/docker-compose.yml" "$owned_workdir/.env" "$owned_workdir/data/state.json")" ]]
printf 'OK signal returns nonzero and cleanup cannot remove a container by name\n'

: >"$docker_log"
if ( fail_start=true; run_join join --workdir "$owned_workdir" --maintenance "${common[@]}" </dev/null ); then exit 1; fi
[[ "$(grep -c "^start $fixture_container_id$" "$docker_log")" == 1 ]]
printf 'OK postcommit start failure remains pending without backup rollback or invite retry\n'

: >"$docker_log"
if run_join rebind --workdir "$owned_workdir" --maintenance "${common[@]}" </dev/null; then exit 1; fi
! grep -q '^stop ' "$docker_log"
run_join rebind --workdir "$owned_workdir" --maintenance --confirm-node-id 22222222-2222-4222-8222-222222222222 --confirm-controller-id 33333333-3333-4333-8333-333333333333 "${common[@]}" </dev/null
grep -q ' node rebind .*--confirm-node-id .*--confirm-controller-id ' "$docker_log"
! grep -q ' node detach' "$docker_log"
printf 'OK rebind uses both exact old IDs and never detach-first\n'

no_secret=("${common[@]:0:${#common[@]}-2}")
: >"$docker_log"
if ( join_open_tty() { return 1; }; run_join join --workdir "$test_dir/no-tty" --fresh "${no_secret[@]}" </dev/null ); then exit 1; fi
[[ ! -e "$test_dir/no-tty" ]]
! grep -q '^stop ' "$docker_log"
for value in 16m 1h 999999999999999999999999999s 0s -1m; do
  if join_timeout_seconds "$value"; then exit 1; fi
done
printf 'OK absent TTY and unbounded timeout are rejected\n'

: >"$docker_log"
if ( fixture_env='"KEEP_CUSTOM=a\nPASSWORD=x"'; run_join join --workdir "$owned_workdir" --maintenance "${common[@]}" </dev/null ); then exit 1; fi
! grep -q '^stop ' "$docker_log"
printf 'OK multiline custom environment is rejected without changing deployment\n'

malicious="node'; \$(touch $test_dir/injected)"
run_join join --workdir "$test_dir/quoted" --mode fresh --name "$malicious" "${common[@]:0:${#common[@]}-4}" --secret-fd 0 </dev/null
[[ ! -e "$test_dir/injected" ]]
printf 'OK public name metacharacters stay arguments\n'
printf 'Installer join checks passed\n'

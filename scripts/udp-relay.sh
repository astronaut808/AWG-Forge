#!/usr/bin/env bash
set -euo pipefail

die() { printf 'relay: %s\n' "$*" >&2; exit 1; }

valid_ipv4() {
  local a b c d
  [[ "$1" =~ ^(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$ ]] || return 1
  IFS=. read -r a b c d <<< "$1"
  (( a > 0 && a < 224 && a != 127 && b <= 255 && c <= 255 && d <= 255 )) || return 1
  [[ "$a.$b" != 169.254 ]]
}

valid_port() {
  [[ "$1" =~ ^[1-9][0-9]{0,4}$ ]] && (( $1 <= 65535 ))
}

ipt() { iptables -w 5 "$@"; }

delete_conntrack() {
  local entries
  if conntrack -D "$@" >/dev/null 2>&1; then
    return 0
  fi
  # conntrack also exits nonzero when there are no entries to delete.
  entries="$(conntrack -L "$@")" && [[ -z "$entries" ]]
}

purge_mapping() {
  local original_ip="$1" original_port="$2" reply_ip="$3" reply_port="$4"
  valid_ipv4 "$original_ip" && valid_port "$original_port" &&
    valid_ipv4 "$reply_ip" && valid_port "$reply_port" || return 1
  delete_conntrack -f ipv4 -p udp --dst-nat --orig-dst "$original_ip" --dport "$original_port" \
    --reply-src "$reply_ip" --reply-port-src "$reply_port"
}

# Recover the tuple from an owned rule when cleaning up a legacy container.
purge_rule_mapping() {
  local original_ip="" original_port="" reply_ip="" reply_port=""
  while (( $# > 1 )); do
    case "$1" in
      -d|--ctorigdst) original_ip="${2%/32}"; shift ;;
      --dport|--ctorigdstport) original_port="$2"; shift ;;
      --to-destination) reply_ip="${2%:*}"; reply_port="${2##*:}"; shift ;;
      --ctreplsrc) reply_ip="${2%/32}"; shift ;;
      --ctreplsrcport) reply_port="$2"; shift ;;
    esac
    shift
  done
  purge_mapping "$original_ip" "$original_port" "$reply_ip" "$reply_port"
}

purge_untranslated() {
  # The new DNAT must be active first. Packets received during replacement
  # may already have a cached mapping to the original local address and port.
  delete_conntrack -f ipv4 -p udp --orig-dst "$local_ip" --dport "$listen_port" \
    --reply-src "$local_ip" --reply-port-src "$listen_port"
}

# Delete only exact rules carrying our owner comment, without shell evaluation.
remove_owned() {
  local table="$1" chain="$2" suffix="$3" listing line
  local -a rule
  listing="$(ipt -t "$table" -S "$chain")" || return 1
  while IFS= read -r line; do
    [[ "$line" == "-A $chain "* ]] || continue
    line="${line/--comment \"$owner-$suffix\" /--comment $owner-$suffix }"
    [[ "$line" != *\"* ]] || continue
    read -r -a rule <<< "$line"
    local i
    for ((i=2; i+1 < ${#rule[@]}; i++)); do
      if [[ "${rule[i]}" == --comment && "${rule[i+1]}" == "$owner-$suffix" ]]; then
        if [[ "$suffix" != dnat && ! -f "$mapping_file" ]]; then
          purge_rule_mapping "${rule[@]:2}" || return 1
        fi
        ipt -t "$table" -D "$chain" "${rule[@]:2}" || return 1
        if [[ "$suffix" == dnat ]] && ! purge_rule_mapping "${rule[@]:2}"; then
          ipt -t "$table" -A "$chain" "${rule[@]:2}" || :
          return 1
        fi
        break
      fi
    done
  done <<< "$listing"
}

lock() { flock -x -w 15 9; }
unlock() { flock -u 9; }

[[ $# == 7 && "$2" =~ ^[0-9a-f]{16}$ ]] || die "invalid relay arguments"
action="$1"
owner="awg-forge-relay-$2"
listen_port="$3"
target_host="$4"
target_ip="$target_host"
target_port="$5"
local_ip="$6"
interface="$7"
if ! valid_port "$listen_port" || ! valid_port "$target_port"; then
  die "invalid UDP port"
fi
dynamic=false
if [[ "$local_ip" == auto && "$interface" == auto ]]; then
  dynamic=true
  host="${target_host%.}"
  label='[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?'
  [[ ${#host} -le 253 && "$host" =~ [a-zA-Z] && "$host" =~ ^$label(\.$label)*$ ]] || die "invalid DNS destination"
else
  valid_ipv4 "$target_ip" && valid_ipv4 "$local_ip" && [[ "$target_ip" != "$local_ip" ]] || die "invalid IPv4 address"
  [[ "$interface" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$ ]] || die "invalid interface"
fi
state_file="/run/$owner"
mapping_dir=/var/lib/awg-forge-relay
mapping_file="$mapping_dir/mapping"

remember_mapping() {
  [[ -d "$mapping_dir" ]] || return 0
  local draft="$mapping_file.next"
  rm -f -- "$draft" || return 1
  (umask 077; set -C; printf '%s %s %s %s\n' "$local_ip" "$listen_port" "$target_ip" "$target_port" >"$draft") &&
    mv -fT -- "$draft" "$mapping_file"
}

purge_saved_mapping() {
  if [[ -e "$mapping_file" || -L "$mapping_file" ]]; then
    local original_ip original_port reply_ip reply_port extra
    [[ -f "$mapping_file" && ! -L "$mapping_file" ]] &&
      read -r original_ip original_port reply_ip reply_port extra <"$mapping_file" &&
      [[ -z "$extra" ]] || return 1
    purge_mapping "$original_ip" "$original_port" "$reply_ip" "$reply_port"
  fi
}

set_rules() {
  local original=(-p udp -m conntrack --ctstate DNAT --ctorigdst "$local_ip" --ctorigdstport "$listen_port" --ctreplsrc "$target_ip" --ctreplsrcport "$target_port")
  dnat=(-i "$interface" -d "$local_ip" -p udp --dport "$listen_port" -m comment --comment "$owner-dnat" -j DNAT --to-destination "$target_ip:$target_port")
  snat=(-o "$interface" "${original[@]}" --ctdir ORIGINAL -m comment --comment "$owner-snat" -j SNAT --to-source "$local_ip")
  out=(-i "$interface" -o "$interface" "${original[@]}" --ctdir ORIGINAL -m comment --comment "$owner-out" -j ACCEPT)
  reply=(-i "$interface" -o "$interface" "${original[@]}" --ctdir REPLY -m comment --comment "$owner-reply" -j ACCEPT)
}

remove_rules() {
  remove_owned nat PREROUTING dnat || return 1
  purge_saved_mapping || return 1
  local failed=0
  remove_owned nat POSTROUTING snat || failed=1
  remove_owned filter FORWARD out || failed=1
  remove_owned filter FORWARD reply || failed=1
  (( failed == 0 )) || return 1
  rm -f -- "$mapping_file" "$mapping_file.next"
}

resolve_destination() {
  local resolved candidate route
  resolved="$(timeout 5 getent ahostsv4 "$target_host")" || return 1
  local addresses
  addresses="$(awk '{print $1}' <<< "$resolved" | LC_ALL=C sort -u)" || return 1
  if [[ "$target_ip" != "$target_host" ]] && grep -Fxq -- "$target_ip" <<< "$addresses"; then
    addresses="$target_ip"$'\n'"$addresses"
  fi
  while IFS= read -r candidate; do
    valid_ipv4 "$candidate" || continue
    route="$(ip -4 route get "$candidate")" || continue
    [[ "$route" != local\ * ]] || continue
    local src dev
    src="$(awk '{for(i=1;i<NF;i++) if ($i=="src") {print $(i+1); exit}}' <<< "$route")"
    dev="$(awk '{for(i=1;i<NF;i++) if ($i=="dev") {print $(i+1); exit}}' <<< "$route")"
    valid_ipv4 "$src" && [[ "$src" != "$candidate" && "$dev" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$ ]] || continue
    target_ip="$candidate"
    local_ip="$src"
    interface="$dev"
    return 0
  done <<< "$addresses"
  return 1
}

forwarding_enabled() {
  [[ "$(cat /proc/sys/net/ipv4/ip_forward)" == 1 ]] ||
    { echo "relay: IPv4 forwarding is disabled" >&2; return 1; }
}

route_matches() {
  local route
  route="$(ip -4 route get "$target_ip")" || return 1
  [[ " $route " == *" dev $interface "* && " $route " == *" src $local_ip "* && "$route" != local\ * ]] ||
    { echo "relay: the route to the target changed" >&2; return 1; }
}

rules_present() {
    ipt -t nat -C PREROUTING "${dnat[@]}" &&
    ipt -t nat -C POSTROUTING "${snat[@]}" &&
    ipt -C FORWARD "${out[@]}" &&
    ipt -C FORWARD "${reply[@]}"
}

status() {
  forwarding_enabled && route_matches && rules_present
}

apply() {
  local listening rules
  forwarding_enabled && route_matches || return 1
  listening="$(ss -Haun "sport = :$listen_port")" || return 1
  [[ -z "$listening" ]] || { echo "relay: UDP port $listen_port is already bound" >&2; return 1; }
  rules="$(ipt -t nat -S)" || return 1
  if awk -v port="$listen_port" -v own="$owner-dnat" '
    $1 == "-A" && ($2 == "PREROUTING" || $2 == "DOCKER") { for (i = 1; i < NF; i++) {
        if ($i == "-p" && $(i+1) == "udp") udp = 1
        if ($i == "--dport" && $(i+1) == port) used = 1
        if ($i == "--comment" && ($(i+1) == own || $(i+1) == "\"" own "\"")) owned = 1
      }
      if (udp && used && !owned) conflict = 1
      udp = used = owned = 0
    }
    END { exit !conflict }
  ' <<< "$rules"; then
    echo "relay: UDP port $listen_port conflicts with a prerouting rule" >&2
    return 1
  fi
  if rules_present 2>/dev/null; then
    # Adopt an existing tuple, but keep older cleanup state if it differs.
    if [[ ! -e "$mapping_file" && ! -L "$mapping_file" ]] ||
      [[ -f "$mapping_file" && ! -L "$mapping_file" &&
        "$(cat -- "$mapping_file")" == "$local_ip $listen_port $target_ip $target_port" ]]; then
      remember_mapping || return 1
      purge_untranslated
      return
    fi
  fi
  remove_rules || return 1
  remember_mapping || return 1
  if ipt -A FORWARD "${reply[@]}" &&
    ipt -A FORWARD "${out[@]}" &&
    ipt -t nat -I POSTROUTING "${snat[@]}" &&
    ipt -t nat -A PREROUTING "${dnat[@]}" &&
    purge_untranslated; then
    return 0
  fi
  remove_rules || printf 'relay: failed to roll back firewall rules\n' >&2
  return 1
}

# nsfs exposes the same network namespace inode to host-network containers.
# A shared lock serializes rule changes with AWG listener activation.
exec 9</proc/self/ns/net
lock || die "could not lock the network namespace"
case "$action" in
  validate)
    if $dynamic; then
      unlock
      resolve_destination || die "DNS destination has no usable IPv4 route"
    fi
    ;;
  status)
    if $dynamic; then
      read -r target_ip local_ip interface < "$state_file" || exit 1
    fi
    set_rules
    status
    ;;
  remove) remove_rules ;;
  serve)
    if $dynamic; then
      unlock
      resolve_destination || die "DNS destination has no usable IPv4 route"
      lock || die "could not lock the network namespace"
    fi
    set_rules
    cleanup() {
      trap '' TERM INT
      lock || die "could not lock the network namespace"
      remove_rules || die "could not remove forwarding rules"
      exit 0
    }
    trap cleanup TERM INT
    apply || die "could not apply forwarding rules"
    if $dynamic; then
      printf '%s %s %s\n' "$target_ip" "$local_ip" "$interface" > "$state_file"
    fi
    unlock
    while :; do
      if $dynamic; then
        sleep 60 &
      else
        sleep 3600 &
      fi
      wait "$!" || :
      $dynamic || continue
      old_ip="$target_ip" old_local="$local_ip" old_interface="$interface"
      if ! resolve_destination; then
        printf 'relay: DNS lookup failed; keeping existing forwarding rules\n' >&2
        continue
      fi
      [[ "$old_ip $old_local $old_interface" != "$target_ip $local_ip $interface" ]] || continue
      lock || die "could not lock the network namespace"
      set_rules
      if apply; then
        printf '%s %s %s\n' "$target_ip" "$local_ip" "$interface" > "$state_file"
      else
        printf 'relay: could not apply the new DNS destination; restoring previous forwarding rules\n' >&2
        target_ip="$old_ip" local_ip="$old_local" interface="$old_interface"
        set_rules
        rules_present 2>/dev/null || apply || die "could not restore forwarding rules"
      fi
      unlock
    done
    ;;
  *) die "expected serve, status, remove, or validate" ;;
esac

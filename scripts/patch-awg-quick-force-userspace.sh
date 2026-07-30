#!/bin/sh
set -eu

target=$1
tmp=$(mktemp)

awk '
  $0 == "add_if() {" { in_add_if = 1 }
  { print }
  in_add_if && $0 ~ /^[[:space:]]*local ret[[:space:]]*$/ {
    print "\tif [[ \"${AWG_QUICK_FORCE_USERSPACE:-0}\" == \"1\" ]]; then"
    print "\t\tcmd \"${WG_QUICK_USERSPACE_IMPLEMENTATION:-amneziawg-go}\" \"$INTERFACE\""
    print "\t\treturn"
    print "\tfi"
    inserted = 1
  }
  in_add_if && $0 == "}" { in_add_if = 0 }
  END { exit !inserted }
' "$target" > "$tmp"

mv "$tmp" "$target"
chmod +x "$target"

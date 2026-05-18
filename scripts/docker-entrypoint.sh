#!/usr/bin/env bash
set -euo pipefail

if ! iptables -V | grep -q nf_tables; then
  echo "error: iptables backend must be nf_tables" >&2
  exit 1
fi

if [ "${1:-}" = "serve" ]; then
  exec awg-forge serve
fi

exec awg-forge "$@"

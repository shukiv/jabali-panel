#!/usr/bin/env bash
# certbot --manual-cleanup-hook for the free jabalihosted.com wildcard cert
# (JAB-213 phase 3b). Removes every challenge TXT at the label's name.
# Idempotent: certbot calls it once per -d name; /v1/acme/cleanup clears all.
set -euo pipefail

ENV=/etc/jabali-panel/hostname.env
[[ -r "$ENV" ]] || exit 0
# Read values WITHOUT executing the file. `source` would run any shell
# metacharacters the hosted service put in a value — this script runs as root
# from a timer/renew hook, so a token containing $(...) or backticks would be
# root code execution on every fire, forever. Parse instead.
jh_env() {
  local line
  line="$(grep -m1 "^$1=" "$ENV" 2>/dev/null)" || return 1
  printf '%s' "${line#*=}"
}
TOKEN="$(jh_env TOKEN)"
API="$(jh_env API)"
: "${TOKEN:?}" "${API:?}"

curl -sS --max-time 30 -o /dev/null \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"${TOKEN}\"}" \
  "${API}/v1/acme/cleanup" || true

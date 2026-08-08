#!/usr/bin/env bash
# jabali-hostname-heartbeat.sh — JAB-213. Daily check-in so the service knows
# this label is still in use (abandoned labels are reclaimed) and so the box
# learns when its public IP changed. Installed only when the box uses a free
# jabalihosted.com hostname (i.e. /etc/jabali-panel/hostname.env exists).
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
FQDN="$(jh_env FQDN)"
[[ -n "${TOKEN:-}" && -n "${API:-}" ]] || exit 0

resp="$(curl -sS --max-time 30 -w $'\n%{http_code}' -H 'Content-Type: application/json' \
        -d "{\"token\":\"${TOKEN}\"}" "${API}/v1/heartbeat" 2>/dev/null)" || exit 0
code="${resp##*$'\n'}"
body="${resp%$'\n'*}"

if [[ "$code" == "403" ]]; then
  logger -t jabali-hostname "heartbeat: label revoked or token invalid — the free hostname ${FQDN:-?} is no longer ours"
  exit 0
fi
if [[ "$code" != "200" ]]; then
  exit 0   # transient; try again tomorrow
fi

if printf '%s' "$body" | python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("ip_moved") else 1)' 2>/dev/null; then
  logger -t jabali-hostname "heartbeat: this server's public IP changed — the free hostname ${FQDN:-?} no longer matches. Re-run the hostname setup to get a new label."
fi

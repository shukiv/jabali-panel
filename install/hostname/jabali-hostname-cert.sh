#!/usr/bin/env bash
# jabali-hostname-cert.sh — issue (or renew) a single wildcard cert for a free
# jabalihosted.com hostname (JAB-213 phase 3b): <label> + *.<label> on ONE
# lineage via DNS-01, so panel + mail.<label> + autoconfig.<label> + every
# preview subdomain are covered by one certificate. Uses the service-backed
# certbot manual hooks (the panel does not control the jabalihosted.com zone;
# the service does, via the box's stored token).
#
# certbot persists the manual hooks in the renewal conf, so `certbot renew`
# (the existing systemd timer) renews this lineage unattended.
#
# Usage: jabali-hostname-cert.sh [--staging]
set -euo pipefail

ENV=/etc/jabali-panel/hostname.env
[[ -r "$ENV" ]] || { echo "no free hostname on this box ($ENV missing)" >&2; exit 1; }
# Read values WITHOUT executing the file. `source` would run any shell
# metacharacters the hosted service put in a value — this script runs as root
# from a timer/renew hook, so a token containing $(...) or backticks would be
# root code execution on every fire, forever. Parse instead.
jh_env() {
  local line
  line="$(grep -m1 "^$1=" "$ENV" 2>/dev/null)" || return 1
  printf '%s' "${line#*=}"
}
FQDN="$(jh_env FQDN)"
EMAIL="$(jh_env EMAIL)"
: "${FQDN:?}" "${EMAIL:?}"

HOOK_DIR=/usr/local/libexec/jabali
AUTH="$HOOK_DIR/certbot-auth-hook.sh"
CLEANUP="$HOOK_DIR/certbot-cleanup-hook.sh"
[[ -x "$AUTH" && -x "$CLEANUP" ]] || { echo "hooks missing under $HOOK_DIR" >&2; exit 1; }

staging=()
[[ "${1:-}" == "--staging" ]] && staging=(--staging)

exec certbot certonly \
  --non-interactive --agree-tos --email "$EMAIL" \
  --preferred-challenges dns \
  --manual \
  --manual-auth-hook "$AUTH" \
  --manual-cleanup-hook "$CLEANUP" \
  --cert-name "$FQDN" \
  -d "$FQDN" -d "*.$FQDN" \
  "${staging[@]}"

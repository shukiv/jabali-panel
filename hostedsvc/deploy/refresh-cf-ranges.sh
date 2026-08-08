#!/usr/bin/env bash
# Refresh the Cloudflare IP ranges in the api.jabalihosted.com nginx vhost
# (JAB-213). The origin's real_ip module trusts CF-Connecting-IP only from
# these ranges; if CF adds a range and this list goes stale, requests from the
# new edge IPs stop being un-wrapped and every registrant behind them gets a CF
# edge IP as their "source" — silently breaking label derivation. Run weekly
# from root cron. Reloads nginx only when the ranges actually changed.
set -euo pipefail

VHOST=/etc/nginx/sites-available/jabalihosted-api.conf
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

{
  echo "# Cloudflare ranges — auto-refreshed $(date -u +%FT%TZ) by refresh-cf-ranges.sh"
  for u in https://www.cloudflare.com/ips-v4 https://www.cloudflare.com/ips-v6; do
    curl -fsS --max-time 20 "$u" | sed 's/^/set_real_ip_from /; s/$/;/'
  done
  echo "real_ip_header CF-Connecting-IP;"
} > "$TMP"

# Sanity: a truncated fetch must never wipe the trust list.
if [[ $(grep -c set_real_ip_from "$TMP") -lt 10 ]]; then
  echo "refresh-cf-ranges: fetched too few ranges ($(grep -c set_real_ip_from "$TMP")) — aborting, keeping existing" >&2
  exit 1
fi

# Replace the block between the markers in the vhost.
START='# --- BEGIN cf-ranges ---'
END='# --- END cf-ranges ---'
if ! grep -qF "$START" "$VHOST"; then
  echo "refresh-cf-ranges: markers not found in $VHOST — nothing to update" >&2
  exit 1
fi

python3 - "$VHOST" "$TMP" "$START" "$END" <<'PY'
import sys
vhost, tmp, start, end = sys.argv[1:5]
body = open(vhost).read()
block = open(tmp).read().strip()
pre, rest = body.split(start, 1)
_, post = rest.split(end, 1)
new = pre + start + "\n" + block + "\n" + end + post
if new != body:
    open(vhost, "w").write(new)
    print("changed")
else:
    print("unchanged")
PY

if nginx -t 2>/dev/null; then
  systemctl reload nginx && echo "refresh-cf-ranges: reloaded nginx"
else
  echo "refresh-cf-ranges: nginx -t FAILED after update — NOT reloading" >&2
  exit 1
fi

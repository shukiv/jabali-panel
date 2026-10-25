#!/usr/bin/env bash
# install/tests/test_free_hostname_ipv4_and_checkin.sh — free jabalihosted.com
# hostname: every call to the hostname service goes over IPv4, the first
# heartbeat is sent only at the end of a successful install, and nothing runs
# the DNS-01 wildcard cert wrapper.
#
# The service labels a box by its IPv4 source and refuses an IPv6 caller, and
# api.jabalihosted.com also answers AAAA: dual-stack boxes got the code email
# and then had every claim refused. The first heartbeat marks the label in use
# at the service (a later claim from the same server and email then gets a new
# suffix instead of the same label back), so it must not fire before the
# install has finished. The wrapper used the panel cert's lineage name and
# spent a second certificate from the base domain's shared quota.
#
# Behavioural for jh_first_checkin (extracted from install.sh), source-level
# for the rest.
#
# Run from repo root:
#     bash install/tests/test_free_hostname_ipv4_and_checkin.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn() { awk -v n="$1" '$0 == n "() {" {p=1} p {print} p && /^}$/ {exit}' install.sh; }

# 1. IPv4 on every service call.
if ! fn jh_post | grep -q 'curl -4 '; then
  echo "FAIL: install.sh jh_post does not force IPv4 (curl -4)"
  fail=1
fi
for f in install/hostname/*.sh; do
  while IFS= read -r line; do
    echo "FAIL: $f calls curl without -4: $line"
    fail=1
  done < <(grep -n -E '(^|[^a-z_-])curl ' "$f" | grep -v -E 'curl -4 ' | grep -v -E '^\s*[0-9]+:\s*#')
done

# 2. jh_first_checkin: starts the heartbeat once, without blocking, only when
#    the box has a free hostname.
body=$(fn jh_first_checkin)
if [[ -z "$body" ]]; then
  echo "FAIL: jh_first_checkin not defined in install.sh"
  fail=1
else
  cat > "$tmp/run.sh" <<SH
set -e
calls="$tmp/calls"
systemctl() { echo "systemctl \$*" >> "\$calls"; }
_ok() { :; }
_warn() { :; }
JH_TOKEN_FILE="\$1"
$body
jh_first_checkin
SH
  : > "$tmp/calls"
  bash "$tmp/run.sh" "$tmp/missing.env" || { echo "FAIL: jh_first_checkin failed without a credential"; fail=1; }
  if [[ -s "$tmp/calls" ]]; then
    echo "FAIL: jh_first_checkin ran systemctl on a box without a free hostname: $(cat "$tmp/calls")"
    fail=1
  fi
  echo "TOKEN=x" > "$tmp/hostname.env"
  bash "$tmp/run.sh" "$tmp/hostname.env" || { echo "FAIL: jh_first_checkin failed with a credential"; fail=1; }
  if ! grep -qx 'systemctl start --no-block jabali-hostname-heartbeat.service' "$tmp/calls"; then
    echo "FAIL: jh_first_checkin did not start the heartbeat (--no-block); calls: $(cat "$tmp/calls")"
    fail=1
  fi
fi

# 3. main() sends it after the install is done, before the "installed" banner.
main_body=$(fn main)
seed=$(grep -n -x '  seed_last_built_sha' <<<"$main_body" | head -1 | cut -d: -f1)
check=$(grep -n -x '  jh_first_checkin' <<<"$main_body" | head -1 | cut -d: -f1)
banner=$(grep -n '_ok "jabali-panel + jabali-agent installed' <<<"$main_body" | head -1 | cut -d: -f1)
if [[ -z "$check" ]]; then
  echo "FAIL: main() never calls jh_first_checkin"
  fail=1
elif [[ -z "$seed" || -z "$banner" || "$check" -lt "$seed" || "$check" -gt "$banner" ]]; then
  echo "FAIL: main() calls jh_first_checkin at line $check, want after seed_last_built_sha ($seed) and before the installed banner ($banner)"
  fail=1
fi

# 4. Nothing in install.sh runs the wildcard wrapper (installing it is fine).
while IFS= read -r line; do
  echo "FAIL: install.sh runs jabali-hostname-cert.sh: $line"
  fail=1
done < <(grep -n 'jabali-hostname-cert\.sh' install.sh | grep -v -E 'install -m 0755 ' | grep -v -E '^\s*[0-9]+:\s*#')

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: free hostname IPv4 + first check-in"
fi
exit "$fail"

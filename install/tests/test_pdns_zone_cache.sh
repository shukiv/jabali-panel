#!/usr/bin/env bash
# install/tests/test_pdns_zone_cache.sh — regression coverage for GH #896
# (new-zone NXDOMAIN) and PowerDNS/pdns#11416 (SIGABRT on concurrent
# zone-cache replace).
#
# Every panel domain gets its own pdns zone, inserted straight into the
# gmysql backend. With the zone cache enabled (default refresh 300 s) the
# new zone does not exist for pdns until the next refresh — 161 s of
# NXDOMAIN measured live on a fresh subdomain. On 4.9 `pdns_control
# rediscover` DOES refresh this cache, but concurrent rediscover + refresh
# runs race AuthZoneCache::replace() and trip the
# `pending->d_replacePending` assertion → SIGABRT → pdns down (observed in
# production on 4.9.16 during bulk zone upserts). zone-cache-refresh-
# interval=0 disables the cache: zone lookups hit gmysql directly (pre-4.5
# behaviour), new zones serve immediately, and updateZoneCache()
# early-returns on !isEnabled() so the assertion can never fire.
#
# Asserts install.sh ships BOTH halves:
#   1. The 01-jabali-mysql.conf template (fresh installs) pins
#      zone-cache-refresh-interval=0.
#   2. ensure_pdns_zone_cache converges the setting onto EXISTING boxes:
#      appends when absent, rewrites when present-with-other-value,
#      no-ops when already correct, and skips hosts without the conf
#      (dns module never installed).
#   3. provision_new_software (the `jabali update` path) calls it —
#      install_powerdns does NOT run on update, so without this call the
#      fix would never reach a box that only ever updates (the #1006
#      ensure_tmp_hardening lesson).
#
# Run from repo root:
#     bash install/tests/test_pdns_zone_cache.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. Fresh-install template carries the setting. ---
template=$(awk '/cat > "\$pdns_conf_new" <<PDNSCONF/,/^PDNSCONF$/' install.sh)
if ! grep -q '^zone-cache-refresh-interval=0$' <<<"$template"; then
  echo "FAIL: 01-jabali-mysql.conf template does not pin zone-cache-refresh-interval=0 — fresh installs get 300 s of new-zone blindness AND the pdns#11416 crash path stays open"
  fail=1
fi

# --- 2. The converger behaves, exercised for real against temp files. ---
fn_src=$(awk '/^ensure_pdns_zone_cache\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_pdns_zone_cache not defined in install.sh"
  exit 1
fi
# Stub the host-touching pieces so the function body runs anywhere.
systemctl() { return 1; }   # "pdns not active" branch — no restarts in tests
_ok()   { :; }
_log()  { :; }
_warn() { :; }
eval "$fn_src"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# The function hardcodes the conf path; re-point it through a wrapper that
# rewrites the local variable via a subshell copy of the function.
run_converger() { # $1 = conf file to converge (or missing)
  local conf="$1"
  ( eval "${fn_src/\/etc\/powerdns\/pdns.d\/01-jabali-mysql.conf/$conf}"
    ensure_pdns_zone_cache )
}

# 2a. Absent line → appended.
conf_a="$tmpdir/a.conf"; printf 'launch=gmysql\n' > "$conf_a"
run_converger "$conf_a"
if ! grep -q '^zone-cache-refresh-interval=0$' "$conf_a"; then
  echo "FAIL: converger did not append the setting to a conf that lacked it"
  fail=1
fi

# 2b. Present with another value → rewritten, not duplicated.
conf_b="$tmpdir/b.conf"; printf 'launch=gmysql\nzone-cache-refresh-interval=10\n' > "$conf_b"
run_converger "$conf_b"
if [[ "$(grep -c '^zone-cache-refresh-interval=' "$conf_b")" != "1" ]] \
   || ! grep -q '^zone-cache-refresh-interval=0$' "$conf_b"; then
  echo "FAIL: converger must rewrite an existing value to 0 without duplicating the key"
  fail=1
fi

# 2c. Already correct → byte-identical (idempotency; a rewrite here would
# restart pdns on every single update).
conf_c="$tmpdir/c.conf"; printf 'launch=gmysql\nzone-cache-refresh-interval=0\n' > "$conf_c"
before=$(cat "$conf_c"); run_converger "$conf_c"
if [[ "$(cat "$conf_c")" != "$before" ]]; then
  echo "FAIL: converger rewrote an already-correct conf — every update would restart pdns"
  fail=1
fi

# 2d. Conf missing (dns module never installed) → no file created.
run_converger "$tmpdir/missing.conf"
if [[ -e "$tmpdir/missing.conf" ]]; then
  echo "FAIL: converger created a pdns conf on a host without the dns module"
  fail=1
fi

# --- 3. The update path actually calls it. ---
upd_src=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -q 'ensure_pdns_zone_cache' <<<"$upd_src"; then
  echo "FAIL: provision_new_software does not call ensure_pdns_zone_cache — existing boxes never get the fix (install_powerdns does not run on update)"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: pdns zone cache disabled on both install paths (GH #896 + PowerDNS/pdns#11416)"

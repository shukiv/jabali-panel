#!/usr/bin/env bash
# install/tests/test_pdns_axfr_deny.sh — regression coverage for JAB-350
# (unauthenticated AXFR of managed zones).
#
# PowerDNS's global allow-axfr-ips ACL controls which peers may zone-transfer.
# jabali previously wrote NO global value, relying on pdns's built-in loopback
# default — but a permissive value from an operator edit, a differing package
# default, or a stale hand-written /etc/powerdns/pdns.conf then leaves EVERY
# managed zone transferable by any internet client (bulk enumeration of
# hostnames, MX/service topology, TXT metadata). Reproduced live: injecting
# allow-axfr-ips=0.0.0.0/0 into the main pdns.conf opened full AXFR; pinning
# allow-axfr-ips=127.0.0.0/8,::1 in the jabali pdns.d drop-in (read after the
# main conf) closed it again while leaving per-zone ALLOW-AXFR-FROM metadata —
# how configured secondaries transfer — intact.
#
# Asserts install.sh ships BOTH halves (the ensure_pdns_zone_cache lesson):
#   1. The 01-jabali-mysql.conf template (fresh installs) pins
#      allow-axfr-ips=127.0.0.0/8,::1.
#   2. ensure_pdns_axfr_deny converges the setting onto EXISTING boxes:
#      appends when absent, rewrites a permissive value, no-ops when already
#      correct, and skips hosts without the conf (dns module never installed).
#   3. provision_new_software (the `jabali update` path) calls it —
#      install_powerdns does NOT run on update, so without this call the fix
#      would never reach a box that only ever updates.
#
# Run from repo root:
#     bash install/tests/test_pdns_axfr_deny.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
want='allow-axfr-ips=127.0.0.0/8,::1'

# --- 1. Fresh-install template carries the setting (and no permissive one). ---
template=$(awk '/cat > "\$pdns_conf_new" <<PDNSCONF/,/^PDNSCONF$/' install.sh)
if ! grep -Fxq "$want" <<<"$template"; then
  echo "FAIL: 01-jabali-mysql.conf template does not pin '$want' — fresh installs rely on pdns's default and a permissive global would open AXFR"
  fail=1
fi
if grep -qE '^allow-axfr-ips=0\.0\.0\.0/0' <<<"$template"; then
  echo "FAIL: template ships a permissive allow-axfr-ips=0.0.0.0/0 — that is the vulnerability, not the fix"
  fail=1
fi

# --- 2. The converger behaves, exercised for real against temp files. ---
fn_src=$(awk '/^ensure_pdns_axfr_deny\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_pdns_axfr_deny not defined in install.sh"
  exit 1
fi
# Stub the host-touching pieces so the function body runs anywhere.
systemctl() { return 1; }   # "pdns not active" branch — no restarts in tests
_ok()   { :; }
_log()  { :; }
_warn() { :; }

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# The function hardcodes the conf path; re-point it through a subshell copy.
run_converger() { # $1 = conf file to converge (or missing)
  local conf="$1"
  ( eval "${fn_src/\/etc\/powerdns\/pdns.d\/01-jabali-mysql.conf/$conf}"
    ensure_pdns_axfr_deny )
}

# 2a. Absent line → appended.
conf_a="$tmpdir/a.conf"; printf 'launch=gmysql\n' > "$conf_a"
run_converger "$conf_a"
if ! grep -Fxq "$want" "$conf_a"; then
  echo "FAIL: converger did not append the AXFR ACL to a conf that lacked it"
  fail=1
fi

# 2b. Present with a PERMISSIVE value → rewritten to loopback, not duplicated.
conf_b="$tmpdir/b.conf"; printf 'launch=gmysql\nallow-axfr-ips=0.0.0.0/0,::/0\n' > "$conf_b"
run_converger "$conf_b"
if [[ "$(grep -c '^allow-axfr-ips=' "$conf_b")" != "1" ]] || ! grep -Fxq "$want" "$conf_b"; then
  echo "FAIL: converger must rewrite a permissive value to loopback without duplicating the key"
  fail=1
fi

# 2c. Already correct → byte-identical (idempotency; a rewrite would restart pdns).
conf_c="$tmpdir/c.conf"; printf 'launch=gmysql\n%s\n' "$want" > "$conf_c"
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
if ! grep -q 'ensure_pdns_axfr_deny' <<<"$upd_src"; then
  echo "FAIL: provision_new_software does not call ensure_pdns_axfr_deny — existing boxes never get the fix (install_powerdns does not run on update)"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: pdns global AXFR ACL pinned to loopback on both install paths (JAB-350)"

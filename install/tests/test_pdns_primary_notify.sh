#!/usr/bin/env bash
# install/tests/test_pdns_primary_notify.sh — regression coverage for GH #2131
# (zone changes never NOTIFY the secondaries).
#
# The agent runs pdns_control notify after every zone push. PowerDNS refuses
# it ("PowerDNS not configured as primary") unless primary=yes, so ns2 and any
# other secondary only caught up at the zone's SOA refresh. primary=yes alone
# would also NOTIFY every host in the zone's NS records — records a tenant
# writes — so only-notify= (empty) must ship with it: NOTIFY then goes only to
# the zone's ALSO-NOTIFY targets, which the panel sets.
#
# Asserts install.sh ships BOTH halves (the ensure_pdns_zone_cache lesson):
#   1. The 01-jabali-mysql.conf template (fresh installs) sets primary=yes
#      and an empty only-notify=.
#   2. ensure_pdns_primary converges both onto EXISTING boxes: appends when
#      absent, rewrites other values without duplicating keys, no-ops when
#      already correct, and skips hosts without the conf.
#   3. provision_new_software (the `jabali update` path) calls it.
#
# Run from repo root:
#     bash install/tests/test_pdns_primary_notify.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. Fresh-install template carries both lines. ---
template=$(awk '/cat > "\$pdns_conf_new" <<PDNSCONF/,/^PDNSCONF$/' install.sh)
for want in 'primary=yes' 'only-notify='; do
  if ! grep -Fxq "$want" <<<"$template"; then
    echo "FAIL: 01-jabali-mysql.conf template does not set '$want'"
    fail=1
  fi
done

# --- 2. The converger behaves, exercised for real against temp files. ---
fn_src=$(awk '/^ensure_pdns_primary\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_pdns_primary not defined in install.sh"
  exit 1
fi
systemctl() { return 1; }   # "pdns not active" branch — no restarts in tests
_ok()   { :; }
_log()  { :; }
_warn() { :; }

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

run_converger() { # $1 = conf file to converge (or missing)
  local conf="$1"
  ( eval "${fn_src/\/etc\/powerdns\/pdns.d\/01-jabali-mysql.conf/$conf}"
    ensure_pdns_primary )
}

# converged FILE: exactly one primary=yes and one empty only-notify=.
converged() {
  [[ "$(grep -c '^primary=' "$1")" == "1" ]] && grep -Fxq 'primary=yes' "$1" &&
    [[ "$(grep -c '^only-notify=' "$1")" == "1" ]] && grep -Fxq 'only-notify=' "$1"
}

# 2a. Absent → appended.
conf_a="$tmpdir/a.conf"; printf 'launch=gmysql\n' > "$conf_a"
run_converger "$conf_a"
if ! converged "$conf_a"; then
  echo "FAIL: converger did not append primary=yes + only-notify= to a conf that lacked them"
  fail=1
fi

# 2b. primary already on but only-notify open to the world → only-notify
# emptied. This is the half that keeps tenant NS hosts from getting NOTIFY.
conf_b="$tmpdir/b.conf"; printf 'launch=gmysql\nprimary=yes\nonly-notify=0.0.0.0/0,::/0\n' > "$conf_b"
run_converger "$conf_b"
if ! converged "$conf_b"; then
  echo "FAIL: converger must empty a world-open only-notify without duplicating keys"
  fail=1
fi

# 2c. primary=no → rewritten, not duplicated.
conf_c="$tmpdir/c.conf"; printf 'launch=gmysql\nprimary=no\n' > "$conf_c"
run_converger "$conf_c"
if ! converged "$conf_c"; then
  echo "FAIL: converger must rewrite primary=no without duplicating the key"
  fail=1
fi

# 2d. Already correct → byte-identical (a rewrite would restart pdns on every update).
conf_d="$tmpdir/d.conf"; printf 'launch=gmysql\nprimary=yes\nonly-notify=\n' > "$conf_d"
before=$(cat "$conf_d"); run_converger "$conf_d"
if [[ "$(cat "$conf_d")" != "$before" ]]; then
  echo "FAIL: converger rewrote an already-correct conf — every update would restart pdns"
  fail=1
fi

# 2e. Conf missing (dns module never installed) → no file created.
run_converger "$tmpdir/missing.conf"
if [[ -e "$tmpdir/missing.conf" ]]; then
  echo "FAIL: converger created a pdns conf on a host without the dns module"
  fail=1
fi

# --- 3. The update path actually calls it. ---
upd_src=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -q 'ensure_pdns_primary' <<<"$upd_src"; then
  echo "FAIL: provision_new_software does not call ensure_pdns_primary — existing boxes never get it (install_powerdns does not run on update)"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: pdns sends NOTIFY to ALSO-NOTIFY targets only, on both install paths (GH #2131)"

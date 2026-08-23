#!/usr/bin/env bash
# install/tests/test_webmail_apparmor_runtime.sh — JAB-379 runtime confinement
# smoke test. Complements the source-level test_webmail_apparmor_confine.sh by
# asserting the LIVE webmail process is actually confined:
#   1. its /proc/<pid>/attr/current carries the jabali-bulwark label, and
#   2. the profile actually BLOCKS a denied action (reads /etc/shadow under the
#      label via a throwaway confined process → EACCES).
#
# This is a BOX verification, not a CI unit test. CI containers have no AppArmor
# and no running webmail, and a fresh install sits in complain during its soak —
# in all those cases it SKIPs cleanly (exit 0) so the install.sh regression gate
# stays green. It only asserts when the profile is actually in enforce.
#
#     bash install/tests/test_webmail_apparmor_runtime.sh
set -uo pipefail   # NOT -e: the guards decide skip-vs-fail explicitly

PROFILE=jabali-bulwark
UNIT=jabali-webmail

skip() { echo "SKIP: $*"; exit 0; }
fail() { echo "FAIL: $*"; exit 1; }

# --- guards: only assert when confinement is actually in effect ---
command -v aa-status >/dev/null 2>&1 || skip "aa-status not present (AppArmor not installed)"
[ -d /sys/kernel/security/apparmor ] || skip "AppArmor LSM not active in kernel"

aa_json="$(aa-status --json 2>/dev/null)" || skip "aa-status failed (AppArmor disabled)"

# The profile must be loaded in ENFORCE — the only mode where a denied action is
# actually blocked. complain (fresh-install soak) → nothing to assert yet.
mode_line="$(printf '%s' "$aa_json" | grep -oE "\"${PROFILE}\":\"[a-z-]+\"" | head -1)"
mode="${mode_line##*:\"}"; mode="${mode%\"}"
[ "$mode" = "enforce" ] || skip "profile ${PROFILE} not enforce (mode=${mode:-absent}); complain soak or unloaded"

pid="$(systemctl show "$UNIT" -p MainPID --value 2>/dev/null)"
{ [ -n "$pid" ] && [ "$pid" != 0 ]; } || skip "${UNIT} not running (lazy-start until first mail domain)"

fails=0

# --- assert 1: the live webmail process carries the jabali-bulwark label ---
label="$(tr -d '\0' < "/proc/$pid/attr/current" 2>/dev/null)"
case "$label" in
  ${PROFILE}*) echo "OK: ${UNIT} pid $pid confined as '$label'" ;;
  *) echo "FAIL: ${UNIT} pid $pid label is '${label:-none}', want '${PROFILE} (enforce)'"; fails=1 ;;
esac

# --- assert 2: the profile actually BLOCKS a denied action ---
# Read /etc/shadow (which the profile hard-denies) under the label via a
# throwaway confined process — proves enforcement non-destructively, without
# touching the real webmail. Require BOTH a non-zero rc AND an EACCES message:
# rc alone would false-pass if aa-exec or the file were missing.
probe_err="$(aa-exec -p "$PROFILE" -- cat /etc/shadow 2>&1 >/dev/null)"; probe_rc=$?
if [ "$probe_rc" -eq 0 ]; then
  echo "FAIL: confined read of /etc/shadow SUCCEEDED — profile is not blocking denies"
  fails=1
elif ! printf '%s' "$probe_err" | grep -qi "permission denied"; then
  echo "FAIL: confined read of /etc/shadow failed but not with EACCES (got: ${probe_err}); block unconfirmed"
  fails=1
else
  echo "OK: confined read of /etc/shadow denied (Permission denied)"
fi

[ "$fails" -eq 0 ] || fail "runtime confinement assertions failed"
echo "PASS: ${UNIT} confined at runtime — label + enforced deny (JAB-379)"

#!/usr/bin/env bash
# install/tests/test_nspawn_orphan_reap.sh — JAB-225.
#
# Deleting a PHP-container user used to leave systemd-nspawn@<user>-php.service
# ENABLED. Current code never creates those units (bubblewrap replaced per-user
# nspawn containers), but boxes carried over from the M13-nspawn era still have
# them enabled for long-deleted accounts. With the account and its machine image
# gone, an enabled unit fails on EVERY boot ("No image for machine
# '<user>-php'") and permanently pads `systemctl --failed`.
#
# The fix has two halves:
#   1. user.delete tears the unit down inline (agent-side; asserted by Go tests
#      in panel-agent/internal/commands/nspawn_reap_test.go).
#   2. reap_orphan_nspawn_php_units() self-heals units orphaned before (1)
#      shipped, on every `jabali update` — asserted here.
#
# Static assertions only (like the JAB-357 socket-group test); the live
# behaviour (fabricate an orphan → converge → reboot → 0 failed units) is the
# .86 box check.
#
# Run from repo root:
#     bash install/tests/test_nspawn_orphan_reap.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# 1. The converger must exist.
if ! grep -qE '^reap_orphan_nspawn_php_units\(\)' install.sh; then
  echo "FAIL: reap_orphan_nspawn_php_units() converger missing — orphaned units never get reaped on existing boxes"
  fail=1
fi

# 2. It must be invoked inside provision_new_software (the `jabali update` sweep),
#    or upgraded hosts (the only ones with residue) never run it.
if ! awk '/^provision_new_software\(\)/{f=1} f&&/reap_orphan_nspawn_php_units/{found=1} f&&/^\}/{exit} END{exit !found}' install.sh; then
  echo "FAIL: reap_orphan_nspawn_php_units is not called from provision_new_software — the self-heal never reaches upgraded hosts"
  fail=1
fi

# 3. The double safety guard must be present: never reap while the OS user still
#    exists, and never reap while a machine image remains (a live container
#    always has one). Losing either guard turns a cleanup into a footgun.
body=$(awk '/^reap_orphan_nspawn_php_units\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if ! grep -qE 'id "\$user"' <<<"$body"; then
  echo "FAIL: reap_orphan_nspawn_php_units lost its OS-user guard (id \"\$user\") — could reap a live account's unit"
  fail=1
fi
if ! grep -qE '/var/lib/machines/' <<<"$body"; then
  echo "FAIL: reap_orphan_nspawn_php_units lost its machine-image guard (/var/lib/machines/...) — could reap a live container"
  fail=1
fi

# 4. Teardown must reset-failed, not just disable: `disable` alone leaves the
#    failed entry in `systemctl --failed` until the next reboot, which is the
#    exact symptom the ticket is about.
if ! grep -qE 'systemctl reset-failed' <<<"$body"; then
  echo "FAIL: reap_orphan_nspawn_php_units does not reset-failed — the failed entry lingers until reboot (JAB-225 symptom)"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: orphan nspawn reaper exists, wired into jabali update, double-guarded, resets failed state (JAB-225)"

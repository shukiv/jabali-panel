#!/usr/bin/env bash
# install/tests/test_backup_retention_after_autoupdate.sh — the backup
# retention sweep runs after the panel self-update, and updated boxes get the
# new schedule.
#
# The retention timer and the default panel self-update both fired at 04:30,
# so after every update the sweep ran the build the update was replacing: a
# retention fix merged on 2026-10-03 only took effect a day after the fleet
# installed it. The timer now fires at 05:00 and the service is ordered after
# jabali-autoupdate.service, so a sweep that comes due while an update is
# running waits for it.
#
# Only install_backup_foundation (fresh install) lays the timer down, so
# ensure_maintenance_isolation — which provision_new_software runs on every
# update — re-copies an existing timer. Checked here by running its real body
# with /etc/systemd/system pointed at a temp dir and install/systemctl stubbed:
#   1. timer present -> it is re-copied from the repo
#   2. timer absent  -> it is not created (never resurrect a removed job)
#
# Run from repo root:
#     bash install/tests/test_backup_retention_after_autoupdate.sh
set -euo pipefail

cd "$(dirname "$0")/../.."
REPO=$(pwd)
fail=0

timer=install/systemd/jabali-backup-retention.timer
service=install/systemd/jabali-backup-retention.service

if ! grep -qx 'OnCalendar=\*-\*-\* 05:00:00' "$timer"; then
  echo "FAIL: $timer must fire at 05:00, after the default 04:30 self-update: $(grep '^OnCalendar' "$timer")"
  fail=1
fi
if ! grep -E '^After=' "$service" | grep -qw 'jabali-autoupdate.service'; then
  echo "FAIL: $service must be ordered After=jabali-autoupdate.service"
  fail=1
fi
if grep -E '^(Requires|Wants|BindsTo)=' "$service" | grep -qw 'jabali-autoupdate.service'; then
  echo "FAIL: $service must only order after the self-update, never pull it in"
  fail=1
fi

body=$(awk '/^ensure_maintenance_isolation\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if [[ -z "$body" ]]; then
  echo "FAIL: ensure_maintenance_isolation() not found in install.sh"
  exit 1
fi

run_case() { # $1=timer present (0/1); prints install calls
  local etc
  etc=$(mktemp -d)
  [[ "$1" == 1 ]] && : >"$etc/jabali-backup-retention.timer"
  (
    REPO_DIR=$REPO
    systemctl() { return 0; }
    # fd 3 carries the recorded calls out of the subshell.
    install() { echo "install $*" >&3; return 0; }
    eval "${body//\/etc\/systemd\/system/$etc}"
    ensure_maintenance_isolation >/dev/null 2>&1
  ) 3>&1
  rm -rf "$etc"
}

out=$(run_case 1)
if ! grep -q "install/systemd/jabali-backup-retention.timer .*/jabali-backup-retention.timer" <<<"$out"; then
  echo "FAIL: an existing retention timer was not re-copied on update, so it keeps the old schedule (install calls: ${out:-none})"
  fail=1
fi

out=$(run_case 0)
if grep -q "jabali-backup-retention.timer" <<<"$out"; then
  echo "FAIL: a missing retention timer was created on update: $out"
  fail=1
fi

if [[ "$fail" == 0 ]]; then
  echo "PASS: backup retention runs at 05:00 after the self-update, and updated boxes get the new timer"
fi
exit "$fail"

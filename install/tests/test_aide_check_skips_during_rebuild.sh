#!/usr/bin/env bash
# install/tests/test_aide_check_skips_during_rebuild.sh — GH #2079.
#
# aide refuses a second run while a baseline rebuild holds the database (exit
# 21, seen on .60), so a check during a rebuild fails. The agent refuses a
# panel-started check during a rebuild, but the daily timer starts
# jabali-aide-check.service on its own. The unit's
# ExecCondition skips the run while jabali-aide-rebuild.service is active, and
# still runs it when systemctl can't answer, so a broken condition never
# silently stops the daily check.
#
# Runs the unit's ExecCondition command against a fake systemctl.
#
# Run from repo root:
#     bash install/tests/test_aide_check_skips_during_rebuild.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
UNIT=install/systemd/jabali-aide-check.service

# The unit name the agent starts the rebuild in (agentwire.AideRebuildUnit).
rebuild_unit=$(sed -n 's/^const AideRebuildUnit = "\(.*\)"$/\1/p' agentwire/aide.go)
if [[ -z "$rebuild_unit" ]]; then
  echo "FAIL: AideRebuildUnit not found in agentwire/aide.go"
  exit 1
fi

cond=$(sed -n 's/^ExecCondition=//p' "$UNIT")
if [[ -z "$cond" ]]; then
  echo "FAIL: $UNIT has no ExecCondition — the daily check runs during a rebuild"
  exit 1
fi
grep -qF "$rebuild_unit" <<<"$cond" || { echo "FAIL: ExecCondition doesn't name $rebuild_unit: $cond"; fail=1; }

# is-active loads a unit that isn't loaded; for the transient rebuild unit,
# gone once it ends, systemd then logs "Failed to open
# /run/systemd/transient/…" (seen on .60). list-units doesn't load it.
if grep -q "is-active" <<<"$cond"; then
  echo "FAIL: ExecCondition uses systemctl is-active; read the state with list-units"; fail=1
fi

# Run the condition with a fake systemctl: FAKE=active lists the rebuild unit
# as running, FAKE=none lists nothing, FAKE=error fails.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/systemctl" <<EOF
#!/bin/sh
case "\$FAKE" in
  active) echo "$rebuild_unit loaded active running Jabali AIDE baseline rebuild" ;;
  error) exit 1 ;;
esac
EOF
chmod +x "$tmp/systemctl"
# ExecCondition=/bin/sh -c '<script>': run the script part the same way.
script=$(sed -n "s/^\/bin\/sh -c '\(.*\)'$/\1/p" <<<"$cond")
if [[ -z "$script" ]]; then
  echo "FAIL: ExecCondition isn't /bin/sh -c '…': $cond"
  exit 1
fi
cond_rc() { FAKE="$1" PATH="$tmp:$PATH" /bin/sh -c "$script" >/dev/null && echo 0 || echo $?; }

# systemd skips the unit when ExecCondition exits 1-254, unless that code is a
# clean exit: SuccessExitStatus= applies to ExecCondition too. On .60 the
# condition exited 1, which this unit's SuccessExitStatus=0 1 … 7 counts as
# success, so the check ran during the rebuild anyway.
success=" 0 $(sed -n 's/^SuccessExitStatus=//p' "$UNIT") "
skips() { [[ "$1" -ge 1 && "$1" -le 254 && "$success" != *" $1 "* ]]; }
runs() { [[ "$success" == *" $1 "* ]]; }

rc=$(cond_rc active)
skips "$rc" || { echo "FAIL: the check runs while the rebuild unit is active (condition exit $rc; clean exits:$success)"; fail=1; }
rc=$(cond_rc none)
runs "$rc" || { echo "FAIL: the check is skipped while no rebuild runs (condition exit $rc)"; fail=1; }
rc=$(cond_rc error)
runs "$rc" || { echo "FAIL: the check is skipped when systemctl can't answer (condition exit $rc)"; fail=1; }

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: the daily AIDE check skips while a baseline rebuild runs (GH #2079)"

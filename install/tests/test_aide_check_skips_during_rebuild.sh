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

# Run the condition with a fake systemctl that exits with $FAKE_RC.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/systemctl" <<'EOF'
#!/bin/sh
exit "$FAKE_RC"
EOF
chmod +x "$tmp/systemctl"
# ExecCondition=/bin/sh -c '<script>': run the script part the same way.
script=$(sed -n "s/^\/bin\/sh -c '\(.*\)'$/\1/p" <<<"$cond")
if [[ -z "$script" ]]; then
  echo "FAIL: ExecCondition isn't /bin/sh -c '…': $cond"
  exit 1
fi
run_cond() { FAKE_RC="$1" PATH="$tmp:$PATH" /bin/sh -c "$script"; }

# systemd: ExecCondition exit 0 runs the unit, 1-254 skips it.
if run_cond 0; then
  echo "FAIL: the check runs while the rebuild unit is active"; fail=1
fi
run_cond 3 || { echo "FAIL: the check is skipped while no rebuild runs"; fail=1; }
run_cond 1 || { echo "FAIL: the check is skipped when systemctl can't answer"; fail=1; }

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: the daily AIDE check skips while a baseline rebuild runs (GH #2079)"

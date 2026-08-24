#!/usr/bin/env bash
# install/tests/test_maintenance_isolation_and_swap.sh — JAB-273.
#
# newaramaapp wedged into a load-385 kswapd death-spiral: a nightly all-tenant
# maintenance sweep filled RAM on a ZERO-swap box, the kernel had nowhere to
# evict to, and sshd could not complete a login handshake — the operator was
# locked out mid-incident. The durable fixes are:
#   1. fleet swap on every box (kswapd gets an exit → graceful degradation);
#   2. every all-tenant maintenance job contained in a memory-capped slice so a
#      sweep can never take the whole box.
#
# Static assertions only (like the JAB-357 / JAB-225 tests); the live behaviour
# (swap active + cgroup caps enforced) is the .86 box check.
#
# Run from repo root:
#     bash install/tests/test_maintenance_isolation_and_swap.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
SLICE=install/systemd/jabali-maintenance.slice
UNITS=(jabali-cache-doctor jabali-disk-maintenance jabali-backup-retention
       jabali-aide-check jabali-sso-reaper jabali-retention-sweep)

# 1. The slice exists and caps memory as a PERCENTAGE (a fixed absolute is no
#    protection on a small box and starves a large one).
if [[ ! -f "$SLICE" ]]; then
  echo "FAIL: $SLICE missing — nothing contains the maintenance cohort"
  fail=1
else
  grep -qE '^MemoryMax=[0-9]+%' "$SLICE" || { echo "FAIL: slice MemoryMax must be a percentage"; fail=1; }
  grep -qE '^MemoryHigh=[0-9]+%' "$SLICE" || { echo "FAIL: slice missing MemoryHigh percentage"; fail=1; }
  grep -qE '^CPUWeight=' "$SLICE" || { echo "FAIL: slice missing CPUWeight"; fail=1; }
  grep -qE '^IOWeight=' "$SLICE" || { echo "FAIL: slice missing IOWeight"; fail=1; }
fi

# 2. Every all-tenant maintenance unit joins the slice and yields (nice + idle IO).
for u in "${UNITS[@]}"; do
  f="install/systemd/${u}.service"
  if [[ ! -f "$f" ]]; then
    echo "FAIL: $f missing"; fail=1; continue
  fi
  grep -qxE 'Slice=jabali-maintenance\.slice' "$f" || { echo "FAIL: $u not in jabali-maintenance.slice (JAB-273)"; fail=1; }
  grep -qxE 'Nice=19' "$f"                          || { echo "FAIL: $u missing Nice=19"; fail=1; }
  grep -qxE 'IOSchedulingClass=idle' "$f"           || { echo "FAIL: $u missing IOSchedulingClass=idle"; fail=1; }
done

# 3. Both convergers exist.
for fn in ensure_fleet_swap ensure_maintenance_isolation; do
  grep -qE "^${fn}\(\)" install.sh || { echo "FAIL: ${fn}() missing from install.sh"; fail=1; }
done

# 4. Both convergers run on BOTH paths: full install (main) AND update
#    (provision_new_software) — a fleet box only self-heals on update.
for fn in ensure_fleet_swap ensure_maintenance_isolation; do
  awk -v F="$fn" '/^main\(\)/{m=1} m&&$0 ~ ("(^| )"F"( |$)"){print} m&&/^}/{exit}' install.sh | grep -q "$fn" \
    || { echo "FAIL: ${fn} not called from main() — fresh installs miss it"; fail=1; }
  awk -v F="$fn" '/^provision_new_software\(\)/{p=1} p&&$0 ~ ("(^| )"F"( |$)"){print} p&&/^}/{exit}' install.sh | grep -q "$fn" \
    || { echo "FAIL: ${fn} not called from provision_new_software() — the fleet never self-heals on update"; fail=1; }
done

# 5. Swap converger safety: only acts below a swap floor (never resizes live
#    swap) and rm's the file on any failure (no half-baked swapfile).
body=$(awk '/^ensure_fleet_swap\(\)/{f=1} f{print} f&&/^}/{exit}' install.sh)
grep -qE 'cur_swap_kb -ge 2097152' <<<"$body" || { echo "FAIL: ensure_fleet_swap lost its 'already has swap' guard — could disturb live swap"; fail=1; }
grep -qE 'rm -f "\$swap_file"' <<<"$body"      || { echo "FAIL: ensure_fleet_swap must rm the swapfile on failure (no half state)"; fail=1; }

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: maintenance cohort memory-capped in its slice + fleet-swap converger wired on install AND update (JAB-273)"

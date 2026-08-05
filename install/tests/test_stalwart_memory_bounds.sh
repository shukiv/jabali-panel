#!/usr/bin/env bash
# install/tests/test_stalwart_memory_bounds.sh — regression coverage for
# JAB-216: serial cPanel restores on a 7.7 GB host drove it into repeated
# global OOM kills and finally a full userspace wedge (sshd and nginx both
# dead, provider-console reboot required). The kernel's last kill was
# stalwart at ~888 MB anon-rss while ingesting migrated mail, and Stalwart
# ran with MemoryHigh=infinity / MemoryMax=infinity — nothing stood between
# a heavy ingest and the host's last free page.
#
# Asserts install.sh ships:
#   1. The sizing brackets, executed at every interesting host size.
#   2. MemoryHigh strictly below MemoryMax at ALL sizes — a soft limit at or
#      above the hard one is silently useless.
#   3. The bracket that actually covers the incident host.
#   4. The drop-in written with both limits and MemoryAccounting.
#   5. Bounds applied to the RUNNING unit, not deferred to a restart.
#   6. Both wiring points: fresh install, and `jabali update` self-heal for
#      the existing fleet that has the unbounded unit today.
#
# Run from repo root:
#     bash install/tests/test_stalwart_memory_bounds.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. Pull the pure arithmetic out of install.sh and run it. ---
# Sourcing all of install.sh would execute its top-level setup, so extract
# just the function under test.
fn_src=$(awk '/^stalwart_mem_bounds_mb\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: stalwart_mem_bounds_mb not defined in install.sh"
  exit 1
fi
eval "$fn_src"

check() {
  local mem_mb="$1" want_high="$2" want_max="$3" label="$4"
  local got_high got_max
  read -r got_high got_max < <(stalwart_mem_bounds_mb "$mem_mb")
  if [[ "$got_high" != "$want_high" || "$got_max" != "$want_max" ]]; then
    echo "FAIL: ${mem_mb}MB host ($label): got High=${got_high}M Max=${got_max}M, want High=${want_high}M Max=${want_max}M"
    fail=1
  fi
}

#     host    High   Max    why
check 1024    512    768    "both floors bind"
check 2048    512    819    "High floor binds, Max is 40%"
check 4096    1024   1638   "straight percentages"
check 7884    1971   3153   "the incident host (7.7 GB)"
check 16384   4096   6144   "both ceilings bind"
check 65536   4096   6144   "large host stays at the ceilings"

# --- 2. The soft limit must stay under the hard one at EVERY size. ---
# A MemoryHigh >= MemoryMax means the kernel reaches the kill threshold
# without ever having applied reclaim pressure, which removes the only
# mechanism that degrades gracefully.
for mb in 256 512 768 1024 1536 2048 3072 4096 6144 7884 8192 12288 16384 32768 65536 131072; do
  read -r h m < <(stalwart_mem_bounds_mb "$mb")
  if [[ "$h" -ge "$m" ]]; then
    echo "FAIL: ${mb}MB host: MemoryHigh=${h}M is not below MemoryMax=${m}M"
    fail=1
  fi
  if [[ "$h" -le 0 || "$m" -le 0 ]]; then
    echo "FAIL: ${mb}MB host: non-positive bound High=${h}M Max=${m}M"
    fail=1
  fi
done

# --- 3. The incident host would have been throttled before it died. ---
# Stalwart was killed at ~888 MB anon-rss on a 7.7 GB box. MemoryHigh must
# land above idle usage (~100 MB) and below that fatal figure's headroom, so
# reclaim starts well before the host runs out.
read -r inc_high inc_max < <(stalwart_mem_bounds_mb 7884)
if [[ "$inc_high" -lt 512 ]]; then
  echo "FAIL: incident host MemoryHigh=${inc_high}M would throttle normal operation (idle is ~100M)"
  fail=1
fi
if [[ "$inc_max" -ge 7884 ]]; then
  echo "FAIL: incident host MemoryMax=${inc_max}M does not bound anything below host RAM"
  fail=1
fi

# --- 4. Drop-in carries both limits and enables accounting. ---
for needle in '20-jabali-memory.conf' 'MemoryAccounting=yes' 'MemoryHigh=${high_mb}M' 'MemoryMax=${max_mb}M'; do
  if ! grep -Fq "$needle" install.sh; then
    echo "FAIL: drop-in missing: $needle"
    fail=1
  fi
done

# --- 5. Applied live, not deferred to a restart. ---
# Bouncing Stalwart to pick up a limit drops live IMAP/JMAP sessions, and
# mid-migration would restart the very service doing the ingest.
if ! grep -q 'set-property --runtime jabali-stalwart.service' install.sh; then
  echo "FAIL: bounds are not applied to the running unit (set-property --runtime missing)"
  fail=1
fi
# Scoped to this function's body — install.sh restarts Stalwart legitimately
# elsewhere (binary upgrade, plan re-apply); it is a limit change that must
# not.
body=$(awk '/^bound_stalwart_memory\(\) \{$/,/^\}$/' install.sh)
if grep -q 'systemctl restart' <<<"$body"; then
  echo "FAIL: bound_stalwart_memory restarts Stalwart; a limit change must not drop live sessions"
  fail=1
fi

# --- 6. Wired on BOTH paths. ---
# Fresh install alone would leave every existing host — the ones that have
# the unbounded unit right now — unprotected.
calls=$(grep -cE '^\s+bound_stalwart_memory( \|\||$)' install.sh || true)
if [[ "$calls" -lt 2 ]]; then
  echo "FAIL: bound_stalwart_memory called $calls time(s); expected >= 2 (fresh install + update self-heal)"
  fail=1
fi
if ! grep -q 'declare -f bound_stalwart_memory' install.sh; then
  echo "FAIL: not wired into the update self-heal path (no declare -f guard)"
  fail=1
fi

# --- 7. Ordering: bound before the unit is started. ---
unit_install=$(grep -n 'install -m 0644 -o root -g root "\${REPO_DIR}/install/systemd/jabali-stalwart.service"' install.sh | head -n1 | cut -d: -f1 || true)
bound_call=$(grep -n '^\s\+bound_stalwart_memory ||' install.sh | head -n1 | cut -d: -f1 || true)
enable_call=$(grep -n 'enabling + starting jabali-stalwart.service' install.sh | head -n1 | cut -d: -f1 || true)
if [[ -z "$unit_install" || -z "$bound_call" || -z "$enable_call" ]]; then
  echo "FAIL: could not locate install/bound/start sequence (unit=$unit_install bound=$bound_call start=$enable_call)"
  fail=1
elif [[ "$bound_call" -le "$unit_install" || "$bound_call" -ge "$enable_call" ]]; then
  echo "FAIL: bound_stalwart_memory (line $bound_call) must sit between the unit install (line $unit_install) and the start (line $enable_call)"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo
  echo "FAIL: Stalwart memory bounds regressed (JAB-216)"
  exit 1
fi

echo "PASS: Stalwart memory bounds sized, ordered, and wired on both paths"

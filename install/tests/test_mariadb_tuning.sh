#!/usr/bin/env bash
# install/tests/test_mariadb_tuning.sh — regression coverage for
# 2026-06-05 puzzle OOM incident: kernel killed mariadbd twice in one
# morning on a 3.8 GB-RAM VPS with no swap because nothing tuned
# innodb_buffer_pool_size against the host's actual RAM.
#
# Asserts install.sh ships:
#   1. A tune_mariadb_for_ram function with the documented bracket map.
#   2. An ensure_swap call early in the installer ordering (not just
#      from build_frontend), so swap exists BEFORE daemons start.
#   3. ensure_swap's threshold is widened to 4 GB (was 2 GB pre-fix).
#   4. tune_mariadb_for_ram wired into the main install ordering.
#
# Run from repo root:
#     bash install/tests/test_mariadb_tuning.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. Function exists; the sizing is EXECUTED, not grepped. ---
#
# This previously asserted four bracket literals (-le 2048/4096/8192/16384).
# #597 replaced the upper brackets with a percentage, so two of them stopped
# matching and this test has been failing ever since — unnoticed, because
# install/tests was not wired into CI. Grepping for constants stops covering
# anything the moment the shape changes; running the arithmetic does not.
if ! grep -q '^tune_mariadb_for_ram() {' install.sh; then
  echo "FAIL: tune_mariadb_for_ram function not defined in install.sh"
  fail=1
fi

fn_src=$(awk '/^mariadb_pool_size_mb\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: mariadb_pool_size_mb not defined in install.sh"
  exit 1
fi
eval "$fn_src"

check_pool() {
  local mem_mb="$1" want="$2" label="$3" got
  got=$(mariadb_pool_size_mb "$mem_mb")
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: ${mem_mb}MB host ($label): pool=${got}M, want ${want}M"
    fail=1
  fi
}

#          host    pool   why
check_pool 1024    128    "tiny VM, OOM-critical"
check_pool 2048    128    "top of the tiny bracket"
check_pool 2049    256    "just over into the puzzle bracket"
check_pool 3891    256    "the puzzle box (3.8 GB) that was OOM-killed twice"
check_pool 4096    256    "top of the puzzle bracket"
# The 1024 MB floor is unreachable with the brackets as they stand: it binds
# only below ~2926 MB, and everything under 4096 MB is already caught by the
# fixed brackets above. Left in place as a guard for future bracket edits —
# same reason as the one in stalwart_mem_bounds_mb — so this asserts the 35%
# that actually applies rather than the floor that does not.
check_pool 4097    1433   "just over the bracket — 35%, floor does not bind"
check_pool 8192    2867   "35% of RAM"
check_pool 12288   4300   "35% of RAM"

# The pool must never claim the whole box: this shares RAM with panel-api,
# the agent, Stalwart, Kratos, PDNS, CrowdSec and every tenant PHP-FPM pool.
for mb in 512 1024 2048 4096 8192 16384 32768 65536; do
  pool=$(mariadb_pool_size_mb "$mb")
  if [[ "$pool" -ge "$mb" ]]; then
    echo "FAIL: ${mb}MB host: pool=${pool}M is not smaller than host RAM"
    fail=1
  fi
  if [[ $((pool * 100 / mb)) -gt 40 ]]; then
    echo "FAIL: ${mb}MB host: pool=${pool}M is over 40% of RAM, leaving no headroom"
    fail=1
  fi
done

# --- 2. ensure_swap threshold widened to 4 GB. ---
if ! grep -Fq 'if [[ $mem_mb -gt 4096 ]]; then' install.sh; then
  echo "FAIL: ensure_swap threshold not widened to 4 GB"
  fail=1
fi
if grep -Fq 'if [[ $mem_mb -gt 2048 ]]; then' install.sh; then
  echo "FAIL: ensure_swap still uses the old 2 GB threshold"
  fail=1
fi

# --- 3. ensure_swap called early in the installer (NOT only from build_frontend). ---
# build_frontend already calls it. We want at least one OTHER call site
# in the main installer ordering, before mariadb provisioning.
count=$(grep -cE '^\s+ensure_swap\s*$' install.sh || true)
if [[ "$count" -lt 2 ]]; then
  echo "FAIL: ensure_swap called only $count time(s); expected >= 2 (one early + one from build_frontend)"
  fail=1
fi

# --- 4. tune_mariadb_for_ram wired into the main installer. ---
if ! grep -qE '^\s+tune_mariadb_for_ram\s*$' install.sh; then
  echo "FAIL: tune_mariadb_for_ram is defined but never called"
  fail=1
fi

# --- 5. Ordering: tune_mariadb_for_ram comes after install_mariadb_skip_networking. ---
nw=$(grep -n '^\s*install_mariadb_skip_networking\s*$' install.sh | tail -n1 | cut -d: -f1 || true)
tn=$(grep -n '^\s*tune_mariadb_for_ram\s*$' install.sh | tail -n1 | cut -d: -f1 || true)
if [[ -z "$nw" || -z "$tn" || "$tn" -le "$nw" ]]; then
  echo "FAIL: tune_mariadb_for_ram call site (line $tn) must come after install_mariadb_skip_networking (line $nw)"
  fail=1
fi

# --- 6. OOMScoreAdjust drop-in written. ---
if ! grep -q '15-jabali-oom.conf' install.sh; then
  echo "FAIL: OOMScoreAdjust drop-in path not present"
  fail=1
fi
if ! grep -q 'OOMScoreAdjust=-500' install.sh; then
  echo "FAIL: OOMScoreAdjust=-500 not present in install.sh"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo
  echo "FAIL: install.sh MariaDB right-sizing regressed"
  exit 1
fi

echo "PASS: MariaDB right-sizing + early swap call all in place"

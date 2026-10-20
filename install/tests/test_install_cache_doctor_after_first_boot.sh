#!/usr/bin/env bash
# install/tests/test_install_cache_doctor_after_first_boot.sh — a fresh install
# starts the cache-doctor timer only once the panel has made its tables.
#
# jabali-cache-doctor.timer fires OnBootSec=15min. On a box that has been up
# longer than that, `systemctl enable --now` fires it at once. main() did so
# before start_and_verify, whose first panel boot runs the migrations, so the
# first sweep died on "Table 'jabali_panel.cache_doctor_runs' doesn't exist"
# and the unit stayed failed for up to an hour. main() now installs the timer
# after start_and_verify.
#
# Source-level: main()'s order.
#
# Run from repo root:
#     bash install/tests/test_install_cache_doctor_after_first_boot.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

main_src=$(awk '$0 == "main() {" {p=1} p {print} p && /^}$/ {exit}' install.sh)
line_of() { grep -nE "^[[:space:]]+$1\$" <<<"$main_src" | head -1 | cut -d: -f1; }
timer=$(line_of install_cache_doctor_timer)
boot=$(line_of start_and_verify)

if [[ -z "$timer" || -z "$boot" ]]; then
  echo "FAIL: main() must call install_cache_doctor_timer and start_and_verify (timer=${timer:-none}, boot=${boot:-none})"
  echo "RESULT: FAIL"
  exit 1
fi
if (( timer < boot )); then
  echo "FAIL: main() starts the cache-doctor timer (line $timer) before the panel's first boot migrates its tables (start_and_verify, line $boot)"
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: main() starts the cache-doctor timer after the panel's first boot"

#!/usr/bin/env bash
# install/tests/test_fpm_unit_skips_removed_pool.sh — a per-user FPM unit
# whose pool was removed is skipped, not restarted forever.
#
# jabali-fpm@.service restarts on failure every 5 s, which never reaches
# systemd's start limit (5 starts in 10 s). fpm-pre-start fails while the
# instance has no version pin, so a stray start of a removed pool looped
# forever: four journal lines every 5 seconds (one box: 29,000 restarts in
# 40 hours).
#
# Asserts, source-level:
#   1. The unit has an ExecCondition that skips the start when the
#      instance's version pin is missing, run as root ("+": the instance's
#      User= may no longer exist).
#   2. The pin it checks is the one fpm-pre-start reads and the one
#      php.pool.remove deletes.
#
# Run from repo root:
#     bash install/tests/test_fpm_unit_skips_removed_pool.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

unit="install/systemd/jabali-fpm@.service"
pin_dir="/etc/jabali-panel/user-phpver"
fail=0

if ! grep -qxF "ExecCondition=+/usr/bin/test -e ${pin_dir}/%i" "$unit"; then
  echo "FAIL: $unit doesn't skip an instance without a version pin (ExecCondition=+/usr/bin/test -e ${pin_dir}/%i)"
  fail=1
fi

if ! grep -qF "verfile=\"${pin_dir}/\${user}\"" install/systemd/fpm-pre-start; then
  echo "FAIL: fpm-pre-start no longer reads the pin from ${pin_dir}/<instance>; update the unit's ExecCondition"
  fail=1
fi

if ! grep -qF "verPinRoot = \"${pin_dir}\"" panel-agent/internal/commands/php_pool_remove.go; then
  echo "FAIL: php.pool.remove no longer deletes the pin under ${pin_dir}; update the unit's ExecCondition"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_fpm_unit_skips_removed_pool"
fi
exit "$fail"

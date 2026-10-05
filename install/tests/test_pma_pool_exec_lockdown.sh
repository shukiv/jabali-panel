#!/usr/bin/env bash
# install/tests/test_pma_pool_exec_lockdown.sh — the phpMyAdmin/Adminer FPM
# pool (jabali-pma) disables the command-exec functions.
#
# A fleet audit (2026-10-05) found the pool with no disable_functions on every
# box. It runs as www-data, the group that can read every site's files, and
# its jabali-fpm@pma drop-in starts php-fpm directly, so the jabali-fpm-app
# AppArmor profile (attached to the fpm-exec wrapper) never confines it. A
# program started from PHP ignores open_basedir: code execution in phpMyAdmin
# or Adminer could read every tenant's files.
#
# Asserts install.sh ships:
#   1. One php_admin_value[disable_functions] line in the jabali-pma.conf
#      heredoc, and no php_value form a script could see as overridable.
#   2. That line covers the tenant lockdown (the agent's
#      defaultDisableFunctions, GH #401) plus putenv, mail and mb_send_mail
#      (the LD_PRELOAD + sendmail way back to running programs).
#   3. Both wiring points (fresh install and `jabali update`) and a pool
#      restart, so existing boxes pick the line up on update.
#   4. When a php binary is present: PHP really removes every listed function.
#
# Run from repo root:
#     bash install/tests/test_pma_pool_exec_lockdown.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. The pool heredoc carries exactly one admin disable_functions line. ---
pool=$(awk '/jabali-pma\.conf <<'"'"'POOLEOF'"'"'$/{on=1; next} on && /^POOLEOF$/{exit} on' install.sh)
if [[ -z "$pool" ]]; then
  echo "FAIL: jabali-pma.conf heredoc not found in install.sh"
  exit 1
fi
lines=$(grep -cE '^php_admin_value\[disable_functions\][[:space:]]*=' <<<"$pool" || true)
if [[ "$lines" != "1" ]]; then
  echo "FAIL: jabali-pma.conf must set php_admin_value[disable_functions] exactly once (found $lines)"
  fail=1
fi
if grep -qE '^php_value\[disable_functions\]' <<<"$pool"; then
  echo "FAIL: jabali-pma.conf must use php_admin_value, not php_value, for disable_functions"
  fail=1
fi
list=$(sed -nE 's/^php_admin_value\[disable_functions\][[:space:]]*=[[:space:]]*//p' <<<"$pool" | head -1)

# --- 2. It covers the tenant lockdown plus the pma-only extras. ---
agent=$(sed -nE 's/^const defaultDisableFunctions = "([^"]*)"$/\1/p' panel-agent/internal/commands/php_pool_apply.go)
if [[ -z "$agent" ]]; then
  echo "FAIL: agent defaultDisableFunctions not found in php_pool_apply.go"
  exit 1
fi
IFS=',' read -r -a want <<<"${agent},putenv,mail,mb_send_mail"
for f in "${want[@]}"; do
  if [[ ",${list}," != *",${f},"* ]]; then
    echo "FAIL: jabali-pma.conf disable_functions lacks $f"
    fail=1
  fi
done

# --- 3. Wired into fresh install and update, and the pool restarts. ---
fn=$(awk '/^install_phpmyadmin_fpm_pool\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE 'systemctl restart jabali-fpm@pma\.service' <<<"$fn"; then
  echo "FAIL: install_phpmyadmin_fpm_pool must restart jabali-fpm@pma (php_admin_value is read at pool start)"
  fail=1
fi
for caller in main provision_new_software; do
  # Captured first: `awk | grep -q` under pipefail fails when grep exits early.
  body=$(awk "/^${caller}\\(\\) \\{\$/,/^\\}\$/" install.sh)
  if ! grep -qE '(^|[[:space:]&])install_phpmyadmin_fpm_pool([[:space:]]|$)' <<<"$body"; then
    echo "FAIL: ${caller} does not call install_phpmyadmin_fpm_pool"
    fail=1
  fi
done

# --- 4. PHP itself accepts the list and removes every function in it. ---
if command -v php >/dev/null 2>&1 && [[ -n "$list" ]]; then
  left=$(php -n -d "disable_functions=${list}" -r '
    $left = [];
    foreach (explode(",", $argv[1]) as $f) {
      if (function_exists($f)) { $left[] = $f; }
    }
    echo implode(",", $left);' "$list")
  if [[ -n "$left" ]]; then
    echo "FAIL: PHP still defines these functions with the pma list applied: $left"
    fail=1
  fi
else
  echo "SKIP: php not installed — not checking PHP's reading of the list"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "PASS: jabali-pma pool disables the exec functions"

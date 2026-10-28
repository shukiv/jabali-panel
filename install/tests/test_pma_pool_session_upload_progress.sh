#!/usr/bin/env bash
# install/tests/test_pma_pool_session_upload_progress.sh — the phpMyAdmin/
# Adminer FPM pool (jabali-pma) turns session upload progress off.
#
# CVE-2026-115560 (php.net informational advisory, 2026-10-23): php.net
# advises session.upload_progress.enabled=0 until the fixed PHP release. The
# tenant pools get it from install/php/jabali-php-pool.conf.tmpl (pinned by
# the agent's TestPoolTemplateSessionUploadProgressOff); this checks the
# jabali-pma.conf heredoc, whose pool runs as www-data and can read every
# site's files. `jabali update` re-runs install_phpmyadmin_fpm_pool, which
# restarts the pool (test_pma_pool_exec_lockdown.sh checks that wiring).
#
# Asserts:
#   1. Exactly one php_admin_flag[session.upload_progress.enabled] = off line
#      in the heredoc, and no php_flag / php_value form.
#   2. When a php binary is present: PHP reads that value as off.
#
# Run from repo root:
#     bash install/tests/test_pma_pool_session_upload_progress.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

pool=$(awk '/jabali-pma\.conf <<'"'"'POOLEOF'"'"'$/{on=1; next} on && /^POOLEOF$/{exit} on' install.sh)
if [[ -z "$pool" ]]; then
  echo "FAIL: jabali-pma.conf heredoc not found in install.sh"
  exit 1
fi

# --- 1. One admin flag line, set to off, and no overridable form. ---
lines=$(grep -E '^[^;]*session\.upload_progress\.enabled' <<<"$pool" || true)
if [[ "$lines" != "php_admin_flag[session.upload_progress.enabled] = off" ]]; then
  echo "FAIL: jabali-pma.conf must set exactly 'php_admin_flag[session.upload_progress.enabled] = off', got: ${lines:-<none>}"
  fail=1
fi

# --- 2. PHP reads the value as off. ---
if command -v php >/dev/null 2>&1; then
  got=$(php -n -d session.upload_progress.enabled=off -r 'echo var_export((bool) ini_get("session.upload_progress.enabled"), true);' 2>/dev/null || true)
  if [[ "$got" != "false" ]]; then
    echo "FAIL: PHP reads session.upload_progress.enabled=off as ${got:-<error>}, want false"
    fail=1
  fi
else
  echo "SKIP: php not installed — not checking PHP's reading of the value"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "PASS: jabali-pma pool turns session upload progress off"

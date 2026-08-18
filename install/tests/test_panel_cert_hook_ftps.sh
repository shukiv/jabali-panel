#!/usr/bin/env bash
# install/tests/test_panel_cert_hook_ftps.sh — regression coverage for JAB-268
# (FTPS serves a stale certificate after panel cert renewal).
#
# vsftpd reads the panel hostname cert (/etc/jabali/tls/panel.{crt,key}) but has
# no deploy hook of its own. The certbot deploy hook must refresh it after a
# renewal — WITHOUT ever starting FTP, which is the panel's one deliberately-OFF
# service (server_settings.ftp_enabled=0 → vsftpd masked). A reload that starts
# a masked daemon would silently defeat the opt-in.
#
# Asserts, source-level (no host mutation):
#   1. The hook refreshes vsftpd (try-reload-or-restart).
#   2. The refresh is guarded by `is-active --quiet vsftpd`, and the guard
#      precedes the refresh — so a masked/disabled daemon is never touched.
#   3. The hook never starts/unmasks/enables vsftpd (that would start opt-out FTP).
#   4. A refresh failure is surfaced (not swallowed) and names FTPS/vsftpd as the
#      consumer left on the stale certificate.
#
# Run from repo root:
#     bash install/tests/test_panel_cert_hook_ftps.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

hook="install/letsencrypt/jabali-panel-cert.sh"
fail=0

if [[ ! -f "$hook" ]]; then
  echo "FAIL: $hook not found"
  exit 1
fi

# --- 1. the hook refreshes vsftpd ---
if ! grep -q 'try-reload-or-restart vsftpd' "$hook"; then
  echo "FAIL: hook does not refresh vsftpd after renewal — FTPS keeps the stale cert (JAB-268)"
  fail=1
fi

# --- 2. refresh is guarded by is-active, and the guard comes first ---
guard_ln=$(grep -nE 'is-active --quiet vsftpd' "$hook" | head -1 | cut -d: -f1)
reload_ln=$(grep -nE 'try-reload-or-restart vsftpd' "$hook" | head -1 | cut -d: -f1)
if [[ -z "$guard_ln" ]]; then
  echo "FAIL: vsftpd refresh is not guarded by 'is-active --quiet vsftpd' — it could start masked/opt-out FTP"
  fail=1
elif [[ -n "$reload_ln" && "$guard_ln" -ge "$reload_ln" ]]; then
  echo "FAIL: the is-active guard (line $guard_ln) must precede the vsftpd refresh (line $reload_ln)"
  fail=1
fi

# --- 3. the hook must NOT start / unmask / enable vsftpd ---
# Directive lines only — comments explain WHY the daemon is left alone when off.
body=$(grep -vE '^\s*#' "$hook")
for bad in 'systemctl start vsftpd' 'unmask vsftpd' 'enable vsftpd'; do
  if grep -qF "$bad" <<<"$body"; then
    echo "FAIL: hook runs '$bad' — a renewal must never start the opt-in FTP module"
    fail=1
  fi
done

# --- 4. failure is surfaced and identifies the stale consumer ---
if ! grep -qiE 'vsftpd.*(fail|stale)|stale.*cert' "$hook"; then
  echo "FAIL: a vsftpd refresh failure must be logged, naming the consumer left on the stale cert"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "OK: panel cert hook refreshes FTPS without starting disabled FTP (JAB-268)"
else
  exit 1
fi

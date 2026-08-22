#!/usr/bin/env bash
# install/tests/test_apparmor_flip_timer.sh — regression coverage for JAB-349
# (AppArmor stays non-enforcing indefinitely).
#
# jabali daemon profiles load in COMPLAIN mode (audit-only) on fresh installs
# and stay there forever unless an operator manually runs
# `jabali apparmor flip-mature`. So an Internet-facing panel keeps
# non-enforcing MAC for the life of the box. This timer promotes SOAK-CLEAN
# complain profiles to enforce automatically.
#
# It is safe to auto-run because `jabali apparmor flip-mature` flips ONLY a
# profile with ZERO AppArmor denials in the soak window and SKIPS any profile
# still denying (which would EACCES its daemon under enforce — the #705
# crash-loop). This test pins that the timer is shipped, wired into both the
# fresh-install and update paths, and drives the soak-aware command.
#
# Run from repo root:
#     bash install/tests/test_apparmor_flip_timer.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. The installer function is defined and creates the .timer + .service. ---
fn_src=$(awk '/^install_apparmor_flip_timer\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: install_apparmor_flip_timer not defined in install.sh"
  exit 1
fi
if ! grep -q 'jabali-apparmor-flip-mature.timer' <<<"$fn_src"; then
  echo "FAIL: function does not create jabali-apparmor-flip-mature.timer"
  fail=1
fi
if ! grep -q 'jabali-apparmor-flip-mature.service' <<<"$fn_src"; then
  echo "FAIL: function does not create jabali-apparmor-flip-mature.service"
  fail=1
fi

# --- 2. The service drives the SOAK-AWARE command (never a bare --force). ---
if ! grep -qE 'jabali apparmor flip-mature --soak-days [0-9]+' <<<"$fn_src"; then
  echo "FAIL: service must run 'jabali apparmor flip-mature --soak-days N' (soak-clean only)"
  fail=1
fi
if grep -q -- '--force' <<<"$fn_src"; then
  echo "FAIL: the timer must NOT pass --force — that would enforce still-denying profiles and crash-loop the daemon"
  fail=1
fi
# It must enable the timer.
if ! grep -qE 'systemctl enable .*jabali-apparmor-flip-mature.timer' <<<"$fn_src"; then
  echo "FAIL: function must enable the timer"
  fail=1
fi

# --- 3. install_apparmor calls it (fresh install path). ---
ia_src=$(awk '/^install_apparmor\(\) \{$/,/^\}$/' install.sh)
if ! grep -q 'install_apparmor_flip_timer' <<<"$ia_src"; then
  echo "FAIL: install_apparmor does not call install_apparmor_flip_timer"
  fail=1
fi

# --- 4. The update path re-runs install_apparmor, so the timer reaches
#        existing hosts too (install_apparmor is idempotent). ---
upd_src=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -q 'install_apparmor' <<<"$upd_src"; then
  echo "FAIL: provision_new_software (jabali update) does not run install_apparmor — existing hosts never get the auto-promotion timer"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: AppArmor auto-promotion timer shipped + soak-safe + on both install paths (JAB-349)"

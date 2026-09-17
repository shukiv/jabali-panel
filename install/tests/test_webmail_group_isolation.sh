#!/usr/bin/env bash
# install/tests/test_webmail_group_isolation.sh — regression coverage for
# JAB-351 / JAB-357. The internet-facing jabali-webmail service MUST NOT belong
# to the broad `jabali` group: that group reads the panel DB password + TLS
# private keys under /etc/jabali (JAB-351) AND owns the root Agent socket
# /run/jabali/agent.sock (JAB-357), so a webmail compromise would reach panel
# secrets and host root. Webmail's own secrets are all jabali-webmail-owned; it
# needs only jabali-sockets (primary) + jabali-webmail.
#
# Live-verified on the .86 box: before the fix webmail read /etc/jabali/
# db-password and connected to the root agent socket; after it, both fail and
# webmail still runs.
#
# Asserts, source-level (no host mutation):
#   1. install.sh no longer adds jabali-webmail to the broad $SERVICE_USER group.
#   2. The legacy membership is dropped (gpasswd -d jabali-webmail "$SERVICE_USER")
#      inside the dedicated converger ensure_webmail_not_in_panel_group, and that
#      converger is wired into provision_new_software — the actual `jabali update`
#      path (panel-api/cmd/server/update.go sources install.sh and calls
#      provision_new_software; install_bulwark is mail-module-gated via run_if_mail
#      and does NOT run on a plain `jabali update`, so it cannot be the sole home
#      for the removal). install_bulwark must also call the converger for the
#      fresh-install / mail-module-reinstall path.
#   3. The jabali-webmail.service unit pins SupplementaryGroups=jabali-webmail.
#      NOTE: SupplementaryGroups only ADDS groups (systemd.exec 5); it does not
#      drop a stray /etc/group membership — the gpasswd -d removal + the agent
#      peercred UID gate (#1565) are the clamp. This assertion just guards
#      against the unit re-adding a broad group.
#
# Run from repo root:
#     bash install/tests/test_webmail_group_isolation.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
unit=install/systemd/jabali-webmail.service

# --- 1. no broad-group grant on fresh install ---
if grep -qE 'usermod +-a?G? *-?a? *-?G? *"\$SERVICE_USER" +jabali-webmail' install.sh \
   || grep -qE 'usermod .*-G *"?\$SERVICE_USER"? +jabali-webmail' install.sh; then
  echo "FAIL: install.sh still adds jabali-webmail to the broad \$SERVICE_USER group (JAB-351/357)"
  fail=1
fi

# --- 2. converge removal on upgrade + wired into the update path ---
# The removal itself must exist somewhere in install.sh...
if ! grep -qE 'gpasswd -d jabali-webmail "\$SERVICE_USER"' install.sh; then
  echo "FAIL: install.sh must drop the legacy jabali-webmail membership on upgrade (gpasswd -d ...)"
  fail=1
fi
# ...and live inside the dedicated converger ensure_webmail_not_in_panel_group
# (the webmail twin of ensure_stalwart_not_in_panel_group).
conv=$(awk '/^ensure_webmail_not_in_panel_group\(\)/,/^}/' install.sh)
if ! grep -q 'gpasswd -d jabali-webmail' <<<"$conv"; then
  echo "FAIL: the membership removal must live in ensure_webmail_not_in_panel_group()"
  fail=1
fi
# It must be wired into provision_new_software — the actual `jabali update` sweep.
# install_bulwark is mail-module-gated (run_if_mail) and is NOT reached by a plain
# `jabali update`, so wiring only there would never converge upgraded webmail hosts.
if ! awk '/^provision_new_software\(\)/{f=1} f&&/ensure_webmail_not_in_panel_group/{found=1} f&&/^\}/{exit} END{exit !found}' install.sh; then
  echo "FAIL: ensure_webmail_not_in_panel_group is not called from provision_new_software — a plain 'jabali update' never converges webmail"
  fail=1
fi
# install_bulwark must also invoke it (fresh install + mail-module reinstall path).
# Use a herestring, not `awk | grep -q`: under `set -o pipefail`, grep -q closes
# the pipe on first match and awk dies with SIGPIPE, making the pipeline non-zero
# even on a hit — a false FAIL.
bw=$(awk '/^install_bulwark\(\)/,/^}/' install.sh)
if ! grep -q 'ensure_webmail_not_in_panel_group' <<<"$bw"; then
  echo "FAIL: install_bulwark must call ensure_webmail_not_in_panel_group (fresh + mail-install path)"
  fail=1
fi

# --- 3. unit scopes supplementary groups ---
if ! grep -qE '^SupplementaryGroups=jabali-webmail' "$unit"; then
  echo "FAIL: $unit must set SupplementaryGroups=jabali-webmail to drop the broad jabali group at runtime (JAB-351/357)"
  fail=1
fi
# The unit must NOT put webmail's primary Group back to the broad jabali group.
if grep -qE '^Group=jabali$' "$unit"; then
  echo "FAIL: $unit Group= must not be the broad jabali group"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "OK: webmail group-isolation guards hold"
else
  exit 1
fi

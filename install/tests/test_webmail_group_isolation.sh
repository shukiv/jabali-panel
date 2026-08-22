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
#   2. install_bulwark converges upgraded hosts by dropping the legacy membership
#      (gpasswd -d jabali-webmail "$SERVICE_USER"), and it runs on `jabali update`
#      (install_bulwark is called from main()).
#   3. The jabali-webmail.service unit pins SupplementaryGroups=jabali-webmail so
#      a stray /etc/group membership can't leak the `jabali` group in at runtime.
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
if ! grep -qE 'gpasswd -d jabali-webmail "\$SERVICE_USER"' install.sh; then
  echo "FAIL: install.sh must drop the legacy jabali-webmail membership on upgrade (gpasswd -d ...)"
  fail=1
fi
bw=$(awk '/^install_bulwark\(\)/,/^}/' install.sh)
if ! grep -q 'gpasswd -d jabali-webmail' <<<"$bw"; then
  echo "FAIL: the membership removal must live in install_bulwark (runs on jabali update)"
  fail=1
fi
if ! awk '/^main\(\)/,/^}/' install.sh | grep -q 'install_bulwark' \
   && ! grep -qE '^\s*install_bulwark\b' install.sh; then
  echo "FAIL: install_bulwark is not invoked from the main install/update flow"
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

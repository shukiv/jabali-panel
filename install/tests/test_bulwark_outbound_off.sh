#!/usr/bin/env bash
# install/tests/test_bulwark_outbound_off.sh
#
# Bulwark can report anonymous usage and checks hourly for a new version against
# its project's server. Jabali pins and upgrades Bulwark itself, and a hosting
# server should not contact a third party on its own, so both are off in the env
# file, on every host, with or without AppArmor.
#
# Run from repo root:
#     bash install/tests/test_bulwark_outbound_off.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

tmpl=install/bulwark/bulwark.env.tmpl

for line in BULWARK_TELEMETRY=off BULWARK_UPDATE_CHECK=off \
  VERSION_CHECK_DATA_DIR=/var/lib/jabali-webmail/version-check; do
  if ! grep -qxF "$line" "$tmpl"; then
    echo "FAIL: $tmpl does not set $line" >&2
    exit 1
  fi
done

echo "PASS: Bulwark telemetry and update check are off in $tmpl"

#!/usr/bin/env bash
# install/tests/test_root_terminal_apparmor.sh
#
# The root web terminal (ADR-0096) dials the agent's PTY broker from panel-api.
# panel-api runs confined, so its AppArmor profile has to allow the socket the
# code dials, or every terminal session fails with "permission denied" on a host
# in enforce mode.
#
# Run from repo root:
#     bash install/tests/test_root_terminal_apparmor.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

profile=install/apparmor/usr.local.bin.jabali-panel-api
code=panel-api/internal/api/terminal.go

sock=$(grep -oE 'terminalAgentPTYSk[[:space:]]*=[[:space:]]*"[^"]+"' "$code" | grep -oE '"[^"]+"' | tr -d '"')
if [[ -z "$sock" ]]; then
  echo "FAIL: could not read the PTY socket path from $code" >&2
  exit 1
fi

if ! grep -qE "^[[:space:]]*${sock//./\\.}[[:space:]]+rw,[[:space:]]*$" "$profile"; then
  echo "FAIL: $profile does not allow $sock" >&2
  exit 1
fi

echo "PASS: $profile allows $sock"

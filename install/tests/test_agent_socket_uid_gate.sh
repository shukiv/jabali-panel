#!/usr/bin/env bash
# install/tests/test_agent_socket_uid_gate.sh — regression coverage for
# JAB-366 / JAB-357. The main agent socket (/run/jabali/agent.sock) grants
# every privileged command to any peer that can open it; its 0660 root:jabali
# perms are only a GROUP gate, so a service account accidentally left in the
# jabali group (webmail, JAB-351/357) could drive it. The agent supports an
# SO_PEERCRED UID allow-list (server.Config.AllowedUIDs, -allowed-uids flag);
# install.sh must ACTUALLY pass it, or the gate is inert.
#
# Asserts the agent unit ExecStart wires -allowed-uids to the panel user's UID
# plus root, resolved from the live account (not hardcoded).
#
# Run from repo root:
#     bash install/tests/test_agent_socket_uid_gate.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# The agent ExecStart must pass -allowed-uids with the panel uid + root.
exec_line=$(grep -nE '^ExecStart=\$AGENT_BIN_PATH .*jabali-agent|^ExecStart=\$AGENT_BIN_PATH -socket' install.sh | head -1)
if [[ -z "$exec_line" ]]; then
  # fall back: any ExecStart line referencing the agent socket flags
  exec_line=$(grep -nE '^ExecStart=.*-socket .*-gid .*-pty-gid' install.sh | head -1)
fi
if [[ -z "$exec_line" ]]; then
  echo "FAIL: could not find the agent unit ExecStart in install.sh"
  exit 1
fi
if ! grep -qE 'ExecStart=.*-allowed-uids \$\{panel_uid\},0' <<<"$exec_line"; then
  echo "FAIL: agent ExecStart does not pass '-allowed-uids \${panel_uid},0' — the SO_PEERCRED gate is inert (JAB-366/357)"
  echo "  got: $exec_line"
  fail=1
fi

# panel_uid must be resolved from the live service account, not hardcoded.
if ! grep -qE 'panel_uid="\$\(id -u "\$SERVICE_USER"' install.sh; then
  echo "FAIL: panel_uid must be resolved via 'id -u \$SERVICE_USER' (not hardcoded)"
  fail=1
fi

# It must fail closed if the uid can't be resolved (don't ship an empty list
# that silently disables the gate).
if ! grep -qE '\[\[ -n "\$panel_uid" \]\] \|\| _die' install.sh; then
  echo "FAIL: install must _die when panel_uid can't be resolved, not ship an inert gate"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: agent socket SO_PEERCRED UID gate is wired to panel_uid,0 (JAB-366/357)"

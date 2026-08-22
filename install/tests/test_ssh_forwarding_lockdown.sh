#!/usr/bin/env bash
# install/tests/test_ssh_forwarding_lockdown.sh — regression coverage for
# JAB-352 (tenant SSH keys tunnelling into loopback-only panel services).
#
# SSH-enabled hosting users get the restricted /usr/local/bin/jabali-ssh-shell
# as their login shell and are placed in the jabali-ssh-sandbox group. But sshd
# establishes TCP / Unix-socket / agent / X11 / tunnel forwarding BEFORE the
# login shell runs, so the shell sandbox cannot stop a tenant tunnelling
# (ssh -L/-R/-D, -A, -w) into loopback-only services (MariaDB, PostgreSQL, the
# panel Agent socket, unauthenticated profilers). The fix is a managed
# `Match Group jabali-ssh-sandbox` sshd drop-in that disables every forwarding
# channel; box-verified on Debian 13 that a sandbox user reports all forwarding
# off + permitopen none while root is unaffected.
#
# Asserts install.sh ships BOTH halves (the ensure_pdns_* lesson):
#   1. The static drop-in install/ssh/jabali-ssh-sandbox.conf matches the group
#      and disables every forwarding knob.
#   2. ensure_ssh_forwarding_lockdown converges it onto EXISTING boxes: installs
#      when absent/changed, idempotent when current, removes the drop-in if
#      `sshd -t` rejects it (never leave sshd unparseable), skips when the
#      source is missing (dns/ssh module layout differences).
#   3. Both the fresh-install flow (main) and the update path
#      (provision_new_software) call it — install_sftp_sshd_config runs on fresh
#      install only, so without the update call an update-only host stays open.
#
# Run from repo root:
#     bash install/tests/test_ssh_forwarding_lockdown.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
conf=install/ssh/jabali-ssh-sandbox.conf

# --- 1. The drop-in matches the group and closes every forwarding channel. ---
if [[ ! -f "$conf" ]]; then
  echo "FAIL: $conf missing"
  exit 1
fi
if ! grep -qE '^Match Group jabali-ssh-sandbox$' "$conf"; then
  echo "FAIL: $conf does not Match the jabali-ssh-sandbox group"
  fail=1
fi
for d in 'AllowTcpForwarding no' 'AllowStreamLocalForwarding no' 'GatewayPorts no' \
         'AllowAgentForwarding no' 'X11Forwarding no' 'PermitTunnel no' 'PermitOpen none'; do
  if ! grep -qE "^[[:space:]]*${d}$" "$conf"; then
    echo "FAIL: $conf missing directive: $d"
    fail=1
  fi
done
# A ForceCommand here would break the interactive restricted shell.
if grep -qE '^[[:space:]]*ForceCommand' "$conf"; then
  echo "FAIL: $conf must NOT ForceCommand — the restricted login shell must still work"
  fail=1
fi

# --- 2. The converger behaves, exercised for real against temp files. ---
fn_src=$(awk '/^ensure_ssh_forwarding_lockdown\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_ssh_forwarding_lockdown not defined in install.sh"
  exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
dst="$tmpdir/dst.conf"
export REPO_DIR="$tmpdir/repo"
mkdir -p "$REPO_DIR/install/ssh"
cp "$conf" "$REPO_DIR/install/ssh/jabali-ssh-sandbox.conf"

# Stubs: no host mutation, no real sshd/systemctl.
_ok()   { :; }
_log()  { :; }
_warn() { :; }
# `install -m .. -o .. -g .. SRC DST` — copy the last two args (root not needed).
install() { local a=("$@"); cp "${a[-2]}" "${a[-1]}"; }
systemctl() { return 1; }   # not socket-activated; reloads fail → best-effort branch
pgrep() { return 1; }
pkill() { return 1; }
# sshd -t result is controlled by SSHD_T_RC.
sshd() { return "${SSHD_T_RC:-0}"; }

# Re-point the hardcoded dst path at our temp file.
run_lockdown() { ( eval "${fn_src/\/etc\/ssh\/sshd_config.d\/jabali-ssh-sandbox.conf/$dst}"; ensure_ssh_forwarding_lockdown ); }

# 2a. Absent dst → installed.
rm -f "$dst"; SSHD_T_RC=0 run_lockdown
if ! cmp -s "$conf" "$dst"; then
  echo "FAIL: converger did not install the drop-in when it was absent"
  fail=1
fi

# 2b. Already current → byte-identical, no error (idempotent).
before=$(cat "$dst"); SSHD_T_RC=0 run_lockdown
if [[ "$(cat "$dst")" != "$before" ]]; then
  echo "FAIL: converger altered an already-current drop-in (would reload sshd every update)"
  fail=1
fi

# 2c. sshd -t rejects the new file → drop-in removed (sshd never left broken).
rm -f "$dst"; SSHD_T_RC=1 run_lockdown
if [[ -e "$dst" ]]; then
  echo "FAIL: converger kept a drop-in that sshd -t rejected — sshd could be left unparseable"
  fail=1
fi

# 2d. Source missing → no dst created.
rm -rf "$REPO_DIR/install/ssh"; rm -f "$dst"; SSHD_T_RC=0 run_lockdown
if [[ -e "$dst" ]]; then
  echo "FAIL: converger created a drop-in with no source file"
  fail=1
fi

# --- 3. BOTH install paths call it. ---
# Capture the awk output to a variable first: `awk | grep -q` trips
# `set -o pipefail` when grep -q closes the pipe early and awk takes SIGPIPE.
main_src=$(awk '/^main\(\) \{$/,/^\}$/' install.sh)
if ! grep -q 'ensure_ssh_forwarding_lockdown' <<<"$main_src"; then
  echo "FAIL: main() (fresh install) does not call ensure_ssh_forwarding_lockdown"
  fail=1
fi
upd_src=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -q 'ensure_ssh_forwarding_lockdown' <<<"$upd_src"; then
  echo "FAIL: provision_new_software (jabali update) does not call ensure_ssh_forwarding_lockdown — update-only hosts stay open"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: SSH forwarding lockdown shipped on both install paths (JAB-352)"

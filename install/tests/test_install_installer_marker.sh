#!/usr/bin/env bash
# install/tests/test_install_installer_marker.sh — install.sh's main() marks
# itself running, so the panel's module repair waits for it.
#
# install.sh starts the panel (start_and_verify) before it installs Stalwart.
# The panel's module repair saw mail on and Stalwart missing, and installed
# mail beside the installer: the plan was applied twice and Stalwart got two
# SQL directories. main() now keeps "<pid> <starttime>" of its shell in
# /run/jabali-installer.pid, and the agent's system.module.install refuses
# while that names a live process (panel-agent system_module.go reads the same
# path and fields).
#
# Behavioural for the marker functions (extracted from install.sh), source-
# level for main()'s order and the path the agent reads.
#
# Run from repo root:
#     bash install/tests/test_install_installer_marker.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn() { awk -v n="$1" '$0 == n "() {" {p=1} p {print} p && /^}$/ {exit}' install.sh; }
src=""
for f in mark_installer_running clear_installer_marker; do
  body=$(fn "$f")
  if [[ -z "$body" ]]; then
    echo "FAIL: $f not defined in install.sh"
    fail=1
  fi
  src+="$body"$'\n'
done

if [[ "$fail" -eq 0 ]]; then
  # The marker names the shell that ran main(), with the start time the
  # kernel gives it (field 22 of /proc/<pid>/stat). Run under set -e, as
  # install.sh runs.
  out=$(INSTALLER_MARKER="$tmp/jabali-installer.pid" bash -c '
    set -Eeuo pipefail
    eval "$1"
    mark_installer_running
    echo "$$ $(awk "{print \$22}" "/proc/$$/stat")"
  ' _ "$src")
  got=$(cat "$tmp/jabali-installer.pid" 2>/dev/null || echo missing)
  if [[ "$got" != "$out" ]]; then
    echo "FAIL: marker is '$got', want '$out' (pid and start time of the installer's shell)"
    fail=1
  fi

  INSTALLER_MARKER="$tmp/jabali-installer.pid" bash -c 'set -Eeuo pipefail; eval "$1"; clear_installer_marker' _ "$src"
  [[ -e "$tmp/jabali-installer.pid" ]] && { echo "FAIL: clear_installer_marker left the marker"; fail=1; }

  # A marker that can't be written must not end the install.
  if ! INSTALLER_MARKER="$tmp/no-such-dir/jabali-installer.pid" bash -c '
    set -Eeuo pipefail
    trap "exit 9" ERR
    eval "$1"
    mark_installer_running
    clear_installer_marker
  ' _ "$src"; then
    echo "FAIL: an unwritable marker ended the install"
    fail=1
  fi
fi

# The agent reads the path install.sh writes.
sh_path=$(grep -oE '^INSTALLER_MARKER="?/[^"[:space:]]+' install.sh | head -1 | sed 's/^INSTALLER_MARKER="\{0,1\}//')
go_path=$(grep -oE 'serverInstallerMarker = "[^"]+"' panel-agent/internal/commands/system_module.go | sed 's/.*= "//; s/"$//')
if [[ -z "$sh_path" || "$sh_path" != "$go_path" ]]; then
  echo "FAIL: install.sh writes the marker at '${sh_path}', the agent reads '${go_path}'"
  fail=1
fi

# main() marks itself before the panel's first boot and clears the marker as
# its last step.
main_src=$(fn main)
line_of() { grep -nE "^[[:space:]]+$1\$" <<<"$main_src" | head -1 | cut -d: -f1; }
mark=$(line_of mark_installer_running)
boot=$(line_of start_and_verify)
clear=$(line_of clear_installer_marker)
last=$(grep -nE '^[[:space:]]+[a-z_]+$' <<<"$main_src" | tail -1 | cut -d: -f1)
if [[ -z "$mark" || -z "$boot" || "$mark" -ge "$boot" ]]; then
  echo "FAIL: main() doesn't mark the installer running before start_and_verify (mark=${mark:-none}, boot=${boot:-none})"
  fail=1
fi
if [[ -z "$clear" || "$clear" != "$last" ]]; then
  echo "FAIL: main() doesn't clear the marker as its last step (clear=${clear:-none}, last=${last:-none})"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: main() marks the installer running for the agent, and clears it at the end"

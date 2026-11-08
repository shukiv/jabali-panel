#!/usr/bin/env bash
# install/tests/test_update_keeps_checkout_owner.sh — GH #2440. An update
# leaves the checkout's owner alone.
#
# Every `jabali update` sources the fetched install.sh and runs
# ensure_user_and_dirs, under the steps of the binary the server already runs.
# Binaries from #2399 on run git as root on a root-owned checkout; earlier ones
# run it as the service user on a checkout it owns. Either git refuses a
# checkout whose top directory has the other owner. ensure_user_and_dirs
# handed the top directory to the service user, so every update run by a
# binary from #2399 on failed at its build ("dubious ownership").
#
# Checked here:
#   1. ensure_user_and_dirs runs nothing on an existing checkout directory;
#   2. it creates a missing one owned by root;
#   3. the current update step puts the top directory back to root after it,
#      for an update to a commit whose install.sh still hands it over.
#
# Run from repo root:
#     bash install/tests/test_update_keeps_checkout_owner.sh
set -euo pipefail

cd "$(dirname "$0")/../.."
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

ok()  { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }

fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }
src=$(fn ensure_user_and_dirs)
if [[ -z "$src" ]]; then
  echo "FAIL ensure_user_and_dirs() not found in install.sh"
  exit 1
fi
eval "$src"

# Stubs record what the function would run and change nothing on this host.
calls="$tmp/calls"
install() { printf 'install %s\n' "$*" >>"$calls"; }
chown()   { printf 'chown %s\n' "$*" >>"$calls"; }
chmod()   { printf 'chmod %s\n' "$*" >>"$calls"; }
id()      { return 0; }
useradd() { :; }
usermod() { :; }
getent()  { return 1; }
ln()      { :; }
_log() { :; }
_ok()  { :; }
_die() { echo "_die: $*" >&2; exit 1; }

# run_for <repo_dir>: the calls ensure_user_and_dirs makes on <repo_dir>.
run_for() {
  : >"$calls"
  (REPO_DIR="$1" SERVICE_USER=jabali ENV_FILE="$tmp/etc/jabali-panel/env" ensure_user_and_dirs)
  awk -v d="$1" '$NF == d' "$calls"
}

# 1. An existing checkout keeps its owner.
repo="$tmp/opt/jabali-panel"
mkdir -p "$repo"
got=$(run_for "$repo")
if [[ -z "$got" ]]; then
  ok "an existing checkout directory is left alone"
else
  bad "ensure_user_and_dirs changes an existing checkout directory: $got"
fi

# 2. A missing one is created owned by root (no -o/-g: the update and
# install.sh run as root).
missing="$tmp/fresh/jabali-panel"
got=$(run_for "$missing")
if [[ "$got" == "install -d -m 0755 $missing" ]]; then
  ok "a missing checkout directory is created owned by root"
else
  bad "a missing checkout directory is created as: ${got:-nothing}"
fi

# 3. The update step restores root ownership after ensure_user_and_dirs.
step=$(awk '/\{"sync service-user dirs \(ensure_user_and_dirs\)"/ {p=1} p {print} p && /^\t\t\}\},$/ {exit}' \
  panel-api/cmd/server/update.go)
src_line=$(awk '/&& ensure_user_and_dirs"/ {print NR; exit}' <<<"$step")
chown_line=$(awk '/"chown", "root:root", repoDir\)/ {print NR; exit}' <<<"$step")
if [[ -n "$src_line" && -n "$chown_line" && "$chown_line" -gt "$src_line" ]]; then
  ok "the update step puts the checkout back to root after ensure_user_and_dirs"
else
  bad "the update step leaves the checkout to whatever ensure_user_and_dirs did"
fi

exit "$fail"

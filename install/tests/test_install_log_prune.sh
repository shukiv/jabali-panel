#!/usr/bin/env bash
# Rotated install logs are deleted after 8 weeks.
#
# Every install.sh run, and every `source install.sh` step of `jabali update`,
# writes its own /var/log/jabali/install-<ts>.log. logrotate renames each one
# to install-<ts>.log.1 and, since no file keeps that name, `rotate 8` never
# applies: the .1 files were kept forever. prune_old_install_logs deletes
# rotated install logs older than 8 weeks; install_logrotate (run by
# `jabali update`) calls it.
#
# Run from repo root:
#     bash install/tests/test_install_log_prune.sh
set -uo pipefail

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn_src=$(awk '/^prune_old_install_logs\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: prune_old_install_logs not defined in install.sh"
  echo "RESULT: FAIL"
  exit 1
fi
eval "$fn_src"

d="$tmp/jabali"
mkdir -p "$d/terminal"
old=$(date -d '-70 days' '+%Y%m%d%H%M')
recent=$(date -d '-10 days' '+%Y%m%d%H%M')
mk() { : >"$d/$1"; touch -t "$2" "$d/$1"; }
mk install-2026-05-14_05-56-53.log.1 "$old"
mk install-2026-05-14_05-57-00.log.2.gz "$old"
mk install-2026-10-02_04-30-00.log.1 "$recent"
mk install-2026-05-14_06-00-00.log "$old"     # not rotated yet: logrotate's job
mk other-2026-05-14.log.1 "$old"              # not an install log
mk terminal/session.cast "$old"
ln -s install-2026-05-14_06-00-00.log "$d/install.log"

prune_old_install_logs "$d"

for f in install-2026-05-14_05-56-53.log.1 install-2026-05-14_05-57-00.log.2.gz; do
  [[ -e "$d/$f" ]] && { echo "FAIL: rotated install log older than 8 weeks kept: $f"; fail=1; }
done
for f in install-2026-10-02_04-30-00.log.1 install-2026-05-14_06-00-00.log other-2026-05-14.log.1 terminal/session.cast; do
  [[ -e "$d/$f" ]] || { echo "FAIL: prune deleted $f"; fail=1; }
done
[[ -L "$d/install.log" ]] || { echo "FAIL: prune deleted the install.log symlink"; fail=1; }

prune_old_install_logs "$tmp/missing" || { echo "FAIL: prune failed on a host without the directory"; fail=1; }

awk '/^install_logrotate\(\)/{p=1} p&&/prune_old_install_logs/{print} p&&/^}/{exit}' install.sh | grep -q . \
  || { echo "FAIL: install_logrotate (jabali update) doesn't call prune_old_install_logs"; fail=1; }

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: rotated install logs are deleted after 8 weeks"

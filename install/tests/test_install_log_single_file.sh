#!/usr/bin/env bash
# The install log is one file, /var/log/jabali/install.log.
#
# Until 2026-11 every install.sh run, and every `source install.sh` step of
# `jabali update` (about 26 a night), wrote its own install-<ts>.log, and
# logrotate renamed each one to install-<ts>.log.1 and never removed it: one
# box had 735 files in /var/log/jabali. Now every run appends to install.log
# under a header line, and fold_legacy_install_logs (install_logrotate, run by
# `jabali update`) folds the old files into install.log.1.
#
# Run from repo root:
#     bash install/tests/test_install_log_single_file.sh
set -uo pipefail

fail=0
tmp=$(mktemp -d)
trap 'chmod -R u+rwx "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT

extract() { awk -v f="$1" '$0 ~ "^"f"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }
fns="$tmp/fns.sh"
for fn in _install_log_run_label _init_install_log fold_legacy_install_logs; do
  src=$(extract "$fn")
  if [[ -z "$src" ]]; then
    echo "FAIL: $fn not defined in install.sh"
    echo "RESULT: FAIL"
    exit 1
  fi
  printf '%s\n' "$src" >>"$fns"
done
_log() { :; }
# eval, not source: functions defined in this script see BASH_SOURCE == $0,
# as they do in a plain `bash install.sh` run. The sourced case is below.
eval "$(cat "$fns")"

headers() { grep -c '^===== .* install\.sh\[[0-9]*\] ' "$1" 2>/dev/null || true; }

# --- one file, a header per run ---------------------------------------------
d="$tmp/jabali"
mkdir -p "$d"
printf 'old run\n' >"$d/install-2026-11-11_04-32-49.log"
ln -s install-2026-11-11_04-32-49.log "$d/install.log"   # the old "latest" link

_cli_token="tok-SECRET-123" _cli_install_module="" _cli_uninstall="" _cli_dry_run="" JABALI_DEBUG=""
_init_install_log "$d"
[[ "$LOG_FILE" == "$d/install.log" ]] || { echo "FAIL: LOG_FILE=$LOG_FILE, want $d/install.log"; fail=1; }
[[ -f "$d/install.log" && ! -L "$d/install.log" ]] || { echo "FAIL: install.log is not a regular file"; fail=1; }
[[ "$(cat "$d/install-2026-11-11_04-32-49.log")" == "old run" ]] \
  || { echo "FAIL: the header went through the old symlink into install-<ts>.log"; fail=1; }
printf 'line from run 1\n' >>"$LOG_FILE"

_cli_install_module="mail"
_init_install_log "$d"
printf 'line from run 2\n' >>"$LOG_FILE"

n=$(find "$d" -maxdepth 1 -type f -newer "$d/install-2026-11-11_04-32-49.log" -name 'install-*' | wc -l)
[[ "$n" -eq 0 ]] || { echo "FAIL: a run made a new install-* file"; fail=1; }
[[ "$(headers "$d/install.log")" -eq 2 ]] || { echo "FAIL: want 2 run headers:"; cat "$d/install.log"; fail=1; }
grep -q 'install\.sh\[[0-9]*\] run: install =====$' "$d/install.log" || { echo "FAIL: no 'run: install' header"; fail=1; }
grep -q 'run: --install-module mail =====$' "$d/install.log" || { echo "FAIL: no '--install-module mail' header"; fail=1; }
grep -q 'line from run 1' "$d/install.log" && grep -q 'line from run 2' "$d/install.log" \
  || { echo "FAIL: a run's lines are missing"; fail=1; }
grep -q 'tok-SECRET' "$d/install.log" && { echo "FAIL: the --token value reached the log"; fail=1; }
[[ "$(stat -c %a "$d/install.log")" == "600" ]] || { echo "FAIL: install.log mode $(stat -c %a "$d/install.log"), want 600"; fail=1; }

# A `jabali update` step: bash -c "source install.sh && <function>".
bash -c "source '$fns'; _init_install_log '$d' && install_time_sync_stub" 2>/dev/null
grep -q "sourced: source '$fns'; _init_install_log .* install_time_sync_stub =====" "$d/install.log" \
  || { echo "FAIL: a sourced step's header doesn't name the command:"; tail -n 3 "$d/install.log"; fail=1; }

# /var/log/jabali not writable: a per-run file in the fallback directory.
printf 'x' >"$tmp/not-a-dir"
mkdir -p "$tmp/fallback"
_init_install_log "$tmp/not-a-dir/jabali" "$tmp/fallback"
[[ "$LOG_FILE" == "$tmp/fallback/jabali_install-"*.log && -f "$LOG_FILE" ]] \
  || { echo "FAIL: fallback LOG_FILE=$LOG_FILE"; fail=1; }

# --- the old files fold into install.log.1 ----------------------------------
d="$tmp/fold"
mkdir -p "$d/terminal"
printf 'A\n' >"$d/install-2026-09-28_04-30-57.log.1"
printf 'B\n' >"$d/install-2026-10-12_04-30-34.log.1"
printf 'C\n' >"$d/install-2026-11-11_04-32-49.log"
printf 'Z\n' | gzip >"$d/install-2026-05-14_05-57-00.log.2.gz"
printf 'LIVE\n' >"$d/install.log"
printf 'other\n' >"$d/other-2026-05-14.log.1"
printf 'cast\n' >"$d/terminal/session.cast"

# install.sh runs with set -Eeuo pipefail and an ERR trap that exits.
( set -Eeuo pipefail; trap 'exit 99' ERR; fold_legacy_install_logs "$d" ) \
  || { echo "FAIL: fold_legacy_install_logs tripped the ERR trap"; fail=1; }

left=$(find "$d" -maxdepth 1 -name 'install-*' | wc -l)
[[ "$left" -eq 0 ]] || { echo "FAIL: $left install-<ts> files left"; fail=1; }
want=$'\n===== install-2026-05-14_05-57-00.log.2.gz =====\nZ\n\n===== install-2026-09-28_04-30-57.log.1 =====\nA\n\n===== install-2026-10-12_04-30-34.log.1 =====\nB\n\n===== install-2026-11-11_04-32-49.log =====\nC'
[[ "$(cat "$d/install.log.1")" == "$want" ]] \
  || { echo "FAIL: install.log.1 is not the old files, oldest first:"; cat "$d/install.log.1"; fail=1; }
[[ "$(stat -c %a "$d/install.log.1")" == "600" ]] || { echo "FAIL: install.log.1 mode, want 600"; fail=1; }
[[ "$(cat "$d/install.log")" == "LIVE" ]] || { echo "FAIL: the fold touched install.log"; fail=1; }
[[ -f "$d/other-2026-05-14.log.1" && -f "$d/terminal/session.cast" ]] || { echo "FAIL: the fold removed a file that isn't an install log"; fail=1; }

before=$(cat "$d/install.log.1")
fold_legacy_install_logs "$d"
[[ "$(cat "$d/install.log.1")" == "$before" ]] || { echo "FAIL: a second fold changed install.log.1"; fail=1; }

# History already in install.log.1 stays, after the folded files.
d="$tmp/fold2"
mkdir -p "$d"
printf 'A\n' >"$d/install-2026-09-28_04-30-57.log.1"
printf 'unreadable\n' >"$d/install-2026-09-29_04-30-14.log.1"
chmod 000 "$d/install-2026-09-29_04-30-14.log.1"
printf 'ROTATED\n' >"$d/install.log.1"
( set -Eeuo pipefail; trap 'exit 99' ERR; fold_legacy_install_logs "$d" ) \
  || { echo "FAIL: an unreadable old file tripped the ERR trap"; fail=1; }
[[ "$(tail -n 1 "$d/install.log.1")" == "ROTATED" ]] && grep -q '^A$' "$d/install.log.1" \
  || { echo "FAIL: existing install.log.1 history lost or out of order:"; cat "$d/install.log.1"; fail=1; }

fold_legacy_install_logs "$tmp/missing" || { echo "FAIL: fold failed on a host without the directory"; fail=1; }

awk '/^install_logrotate\(\)/{p=1} p&&/fold_legacy_install_logs/{print} p&&/^}/{exit}' install.sh | grep -q . \
  || { echo "FAIL: install_logrotate (jabali update) doesn't call fold_legacy_install_logs"; fail=1; }
grep -q '^_init_install_log "\$LOG_DIR"$' install.sh \
  || { echo "FAIL: install.sh doesn't call _init_install_log at top level"; fail=1; }

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: one install log; old install-<ts>.log files fold into install.log.1"

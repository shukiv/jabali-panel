#!/usr/bin/env bash
# install/tests/test_stalwart_migrate_to_v1.sh — ADR-0179.
#
# migrate_stalwart_to_v1 moves a host's mail data from Stalwart 0.16 to 1.0
# with Stalwart's migration tool. Mail must never be left stopped, and a 1.0
# binary must never start on the 0.16 store:
#   - a failure before the final copy changes nothing (0.16 keeps serving);
#   - a failed or interrupted final copy starts 0.16 again;
#   - 1.0 that does not come up on the copy is switched back to 0.16;
#   - only a completed final copy is switched in, and the 0.16 store and binary
#     are kept beside it;
#   - the installer marker is held throughout (the panel's module repair then
#     waits) and released at the end.
#
# Behavioural: extracts the functions from install.sh and runs them in a
# temporary tree, with a fake stalwart-migrate and fake systemctl / curl.
#
# Run from repo root:
#     bash install/tests/test_stalwart_migrate_to_v1.sh
set -euo pipefail

cd "$(dirname "$0")/../.."
root=$(pwd)

fail=0
# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }
for f in migrate_stalwart_to_v1 _stwm_run _stwm_wait_jmap _stwm_on_exit _installer_marker_held_by_other \
         _fetch_stalwart_migrate _stalwart_mark_store_v1 mark_installer_running clear_installer_marker; do
  src=$(fn "$f")
  if [[ -z "$src" ]]; then
    echo "FAIL: $f() not found in install.sh"
    exit 1
  fi
  eval "$src"
done
for v in STALWART_VERSION STALWART_SOURCE_VERSION STALWART_MIGRATE_VERSION; do
  eval "$(grep -m1 "^${v}=" install.sh)"
done
_STWM_PHASE=idle
_STWM_OWN_MARKER=0

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export T="$tmp"

# --- fakes ---------------------------------------------------------------------
_log() { :; }
_ok() { :; }
_warn() { printf 'WARN %s\n' "$*" >>"$T/msgs"; }
_err() { printf 'ERR %s\n' "$*" >>"$T/msgs"; }
_die() { printf 'DIE %s\n' "$*" >>"$T/msgs"; exit 1; }
# Not root in CI: drop the ownership flags.
install() {
  local -a a=()
  while (($#)); do
    case "$1" in -o|-g) shift 2 ;; *) a+=("$1"); shift ;; esac
  done
  command install "${a[@]}"
}
id() { case "$1" in -u) echo 995 ;; -g) echo 984 ;; *) command id "$@" ;; esac; }
# Free space beside the store: 1 TB unless a case sets FAKE_AVAIL.
df() { printf 'Avail\n%s\n' "${FAKE_AVAIL:-1099511627776}"; }
sleep() { :; }
systemctl() {
  printf '%s\n' "$*" >>"$T/systemctl.log"
  case "$1" in
    is-active)
      # FAKE_STOPPED: Stalwart was not running when the update came.
      if [[ -n "${FAKE_STOPPED:-}" ]]; then echo inactive; return 3; fi
      cat "$T/unit-state" 2>/dev/null || echo active ;;
  esac
  return 0
}
# JMAP: 0.16 answers before the switch; after it (the store stamped as 1.x)
# a case can set what 1.0 answers in jmap-code.
curl() {
  if [[ -f "$STALWART_STORE_STAMP" ]]; then cat "$T/jmap-code" 2>/dev/null || echo 200; else echo 200; fi
}
# FAKE_MOUNTPOINT: the store is a mount point (mail on its own disk).
mountpoint() { [[ -n "${FAKE_MOUNTPOINT:-}" ]]; }
install_stalwart_apply() { printf 'apply\n' >>"$T/systemctl.log"; }
# The 1.0 binary "download": a fake binary reporting the version.
_fetch_stalwart_release() {
  rm -rf "$2"; mkdir -p "$2"
  printf '#!/bin/sh\necho %s\n' "$1" >"$2/stalwart"; chmod +x "$2/stalwart"
}

STALWART_STORE_DIR="$tmp/var/stalwart"
STALWART_CONFIG="$tmp/etc/config.json"
STALWART_OPT_DIR="$tmp/opt/stalwart"
STALWART_BIN_LINK="$tmp/bin/stalwart"
STALWART_STORE_STAMP="$tmp/etc/stalwart-store.generation"
STALWART_MIGRATE_STATE="$tmp/state"
STALWART_MIGRATE_DIR="$tmp/migrate"
INSTALLER_MARKER="$tmp/installer.pid"
export FAKE_MARKER="$INSTALLER_MARKER"
REPO_DIR="$tmp/repo"
mkdir -p "$REPO_DIR/install/stalwart"
cp "$root/install/stalwart/migrate-answers.toml" "$REPO_DIR/install/stalwart/"
cp "$root/install/stalwart.sha256" "$REPO_DIR/install/"

# The fake tool: records its argv and whether the installer marker is held;
# `run` writes into the --target-path-map directory. FAKE_FAIL_AT=prepare|
# run1|run2|final fails that call; FAKE_TERM_AT=final interrupts the
# migration there (as stopping the update's unit would).
mkdir -p "$tmp/migrate/$STALWART_MIGRATE_VERSION"
cat >"$tmp/migrate/$STALWART_MIGRATE_VERSION/stalwart-migrate" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$T/tool.log"
{ [[ -f "$FAKE_MARKER" ]] && echo held || echo none; } >>"$T/marker.log"
cmd="$1"; final=0; map=""
while (($#)); do
  case "$1" in --final) final=1 ;; --target-path-map) map="$2"; shift ;; esac
  shift
done
step="$cmd"
if [[ "$cmd" == run ]]; then
  if ((final)); then step=final; else step="run$(grep -c '^run' "$T/tool.log")"; fi
fi
[[ "${FAKE_TERM_AT:-}" == "$step" ]] && { kill -TERM "$PPID"; exit 1; }
[[ "${FAKE_FAIL_AT:-}" == "$step" ]] && exit 1
if [[ "$cmd" == run ]]; then
  dst="${map#*=}"; mkdir -p "$dst"; echo "$step" >"$dst/copied-by"
fi
exit 0
EOF
chmod +x "$tmp/migrate/$STALWART_MIGRATE_VERSION/stalwart-migrate"

# The fake stalwart-cli answers `get BlobStore` as 0.16.27 and 1.0.2 do;
# FAKE_BLOB sets another blob store type, FAKE_BLOB=fail fails the call.
cat >"$tmp/stalwart-cli" <<'EOF'
#!/usr/bin/env bash
[[ "$1 $2" == "get BlobStore" ]] || { echo "fake cli: unexpected $*" >&2; exit 2; }
[[ "${FAKE_BLOB:-}" == fail ]] && exit 1
printf '{"@type":"%s","id":"singleton"}\n' "${FAKE_BLOB:-Default}"
EOF
chmod +x "$tmp/stalwart-cli"
export JABALI_STALWART_CLI="$tmp/stalwart-cli"

# box: a 0.16.27 host. Store with a 0.16 marker, binary in /opt with share/.
box() {
  rm -rf "$tmp/var" "$tmp/etc" "$tmp/opt" "$tmp/bin" "$tmp/state" "$tmp/installer.pid" \
         "$tmp/msgs" "$tmp/tool.log" "$tmp/marker.log" "$tmp/systemctl.log" "$tmp/unit-state" "$tmp/jmap-code"
  mkdir -p "$tmp/var/stalwart" "$tmp/etc" "$tmp/opt/stalwart/share" "$tmp/bin"
  echo "0.16 data" >"$tmp/var/stalwart/CURRENT"
  printf '{"@type":"RocksDb","path":"%s"}\n' "$STALWART_STORE_DIR" >"$STALWART_CONFIG"
  printf '#!/bin/sh\necho %s\n' "$STALWART_SOURCE_VERSION" >"$tmp/opt/stalwart/stalwart"
  chmod +x "$tmp/opt/stalwart/stalwart"
  echo rules >"$tmp/opt/stalwart/share/spam-filter-rules.json.gz"
  ln -s "$tmp/opt/stalwart/stalwart" "$STALWART_BIN_LINK"
  unset FAKE_FAIL_AT FAKE_TERM_AT FAKE_MOUNTPOINT FAKE_BLOB FAKE_STOPPED
}
run_migration() { local rc=0; migrate_stalwart_to_v1 || rc=$?; echo "$rc"; }
expect() { [[ "$2" == "$3" ]] || { echo "FAIL: $1: got '$2', want '$3'"; fail=1; }; }
serving_version() { "$STALWART_BIN_LINK" 2>/dev/null || echo none; }
store_is() { cat "$STALWART_STORE_DIR/CURRENT" 2>/dev/null || cat "$STALWART_STORE_DIR/copied-by" 2>/dev/null || echo none; }
stopped() { [[ -f "$T/systemctl.log" ]] || { echo 0; return; }; grep -c '^stop jabali-stalwart' "$T/systemctl.log" || true; }
tool_runs() { cat "$T/tool.log" 2>/dev/null | wc -l | tr -d ' '; }
untouched() { # <case>: 0.16 serves its own store, nothing switched, marker gone
  expect "$1: binary" "$(serving_version)" "$STALWART_SOURCE_VERSION"
  expect "$1: store" "$(store_is)" "0.16 data"
  expect "$1: 0.16 copy" "$([[ -e "$STALWART_STORE_DIR-0.16" ]] && echo present || echo absent)" absent
  expect "$1: stamp" "$([[ -f "$STALWART_STORE_STAMP" ]] && echo present || echo absent)" absent
  expect "$1: installer marker" "$([[ -f "$INSTALLER_MARKER" ]] && echo held || echo released)" released
}

# 1. Success: two live passes, then the final copy with Stalwart stopped.
box
expect "success: rc" "$(run_migration)" 0
expect "success: tool calls" "$(cut -d' ' -f1-2 "$T/tool.log" | paste -sd'|')" "prepare --non-interactive|run --source|run --source|run --final"
expect "success: Stalwart stopped first, before the final copy" "$(grep -v '^is-active' "$T/systemctl.log" | grep -m1 -n 'stop jabali' | cut -d: -f1)" 1
expect "success: marker held while the tool ran" "$(sort -u "$T/marker.log" | paste -sd' ')" held
expect "success: binary" "$(serving_version)" "$STALWART_VERSION"
expect "success: store" "$(store_is)" final
# The tool leaves the copy's directory 0755 over 0644 files: other local
# users would read the mail store.
expect "success: store directory mode" "$(stat -c %a "$STALWART_STORE_DIR")" 750
expect "success: 0.16 store kept" "$(cat "$STALWART_STORE_DIR-0.16/CURRENT")" "0.16 data"
expect "success: 0.16 binary kept" "$("$STALWART_OPT_DIR-0.16/stalwart")" "$STALWART_SOURCE_VERSION"
expect "success: spam rules carried over" "$(cat "$STALWART_OPT_DIR/share/spam-filter-rules.json.gz" 2>/dev/null)" rules
expect "success: stamp" "$(cat "$STALWART_STORE_STAMP" 2>/dev/null)" 1
expect "success: convergers ran on 1.0" "$(tail -n1 "$T/systemctl.log")" apply
expect "success: installer marker" "$([[ -f "$INSTALLER_MARKER" ]] && echo held || echo released)" released
expect "success: answers in place" "$(cmp -s "$root/install/stalwart/migrate-answers.toml" "$STALWART_MIGRATE_STATE/answers.toml" && echo same)" same
grep -q -- "--target-path-map $STALWART_STORE_DIR=$STALWART_STORE_DIR-1.0 --owner 995:984" "$T/tool.log" \
  || { echo "FAIL: success: the copy does not go beside the store, owned by jabali-mail"; fail=1; }

# 2-4. A failure while 0.16 serves changes nothing and never stops mail.
for at in prepare run1 run2; do
  box; export FAKE_FAIL_AT=$at
  expect "$at fails: rc" "$(run_migration)" 1
  expect "$at fails: Stalwart stopped" "$(stopped)" 0
  untouched "$at fails"
done

# 5. The final copy fails: Stalwart 0.16 is started again on its own store.
box; export FAKE_FAIL_AT=final
expect "final fails: rc" "$(run_migration)" 1
expect "final fails: started again" "$(tail -n1 "$T/systemctl.log")" "start jabali-stalwart"
untouched "final fails"

# 6. Interrupted during the final copy (the update unit stopped): same.
box; export FAKE_TERM_AT=final
expect "final interrupted: rc" "$(run_migration)" 1
expect "final interrupted: started again" "$(tail -n1 "$T/systemctl.log")" "start jabali-stalwart"
untouched "final interrupted"

# 7. 1.0 does not come up on the copy: switched back to 0.16.
box; echo 000 >"$T/jmap-code"; echo activating >"$T/unit-state"
expect "1.0 down: rc" "$(run_migration)" 1
untouched "1.0 down"
ls -d "$STALWART_STORE_DIR"-1.0.failed-* >/dev/null 2>&1 \
  || { echo "FAIL: 1.0 down: the failed copy was not kept"; fail=1; }
expect "1.0 down: 0.16 started last" "$(tail -n1 "$T/systemctl.log")" "start jabali-stalwart"
if grep -q '^apply' "$T/systemctl.log"; then echo "FAIL: 1.0 down: convergers ran"; fail=1; fi

# 8. 1.0 is running but JMAP is slow: left running, not switched back.
box; echo 000 >"$T/jmap-code"; echo active >"$T/unit-state"
expect "1.0 slow: rc" "$(run_migration)" 1
expect "1.0 slow: binary" "$(serving_version)" "$STALWART_VERSION"
expect "1.0 slow: store" "$(store_is)" final

# 9. A completed copy that was switched back is not retried.
box; mkdir -p "$STALWART_MIGRATE_STATE"; : >"$STALWART_MIGRATE_STATE/jabali-final-done"
expect "switched back before: rc (skipped)" "$(run_migration)" 2
expect "switched back before: tool runs" "$(tool_runs)" 0
untouched "switched back before"

# 10-12. Checks that skip the migration before anything runs.
box; mkdir -p "$STALWART_STORE_DIR-0.16"
expect "left-over 0.16 copy: rc (skipped)" "$(run_migration)" 2
expect "left-over 0.16 copy: tool runs" "$(tool_runs)" 0

box; printf '{"@type":"PostgreSql","host":"db"}\n' >"$STALWART_CONFIG"
expect "not the local RocksDB store: rc (skipped)" "$(run_migration)" 2
expect "not the local RocksDB store: tool runs" "$(tool_runs)" 0

box; FAKE_AVAIL=1048576
expect "no disk for the copy: rc (skipped)" "$(run_migration)" 2
expect "no disk for the copy: tool runs" "$(tool_runs)" 0
grep -q 'MB free' "$T/msgs" || { echo "FAIL: no disk for the copy: no warning naming the space"; fail=1; }
unset FAKE_AVAIL

# 13. Another install run holds the marker: the migration waits for a later update.
box
command sleep 30 & other=$!
read -r -a sf <<<"$(sed 's/^.*) //' "/proc/$other/stat")"
printf '%s %s\n' "$other" "${sf[19]}" >"$INSTALLER_MARKER"
expect "marker held: rc (skipped)" "$(run_migration)" 2
expect "marker held: tool runs" "$(tool_runs)" 0
expect "marker held: marker left to its owner" "$(cut -d' ' -f1 "$INSTALLER_MARKER" 2>/dev/null)" "$other"
kill "$other" 2>/dev/null || true

# 14-18. Layouts the migration can't copy or switch: skipped before anything
# runs, mail never stopped, with a warning saying why.
# skipped <case> <warning text>
skipped() {
  expect "$1: rc (skipped)" "$(run_migration)" 2
  expect "$1: tool runs" "$(tool_runs)" 0
  expect "$1: Stalwart stopped" "$(stopped)" 0
  untouched "$1"
  grep -q -- "$2" "$T/msgs" 2>/dev/null || { echo "FAIL: $1: no warning saying '$2'"; fail=1; }
}

# Mail on its own disk, mounted at the store: a mount point can't be renamed.
box; export FAKE_MOUNTPOINT=1
skipped "store is a mount point" "mount point"

# The store is a symlink to another disk: du would measure the link, and the
# copy would land on the disk under /var/lib.
box; mkdir -p "$tmp/disk"; mv "$STALWART_STORE_DIR" "$tmp/disk/stalwart"; ln -s "$tmp/disk/stalwart" "$STALWART_STORE_DIR"
skipped "store is a symlink" "symlink"
rm -rf "$tmp/disk"

# Blobs in a store of their own (a directory or S3) are outside the copy.
box; export FAKE_BLOB=FileSystem
skipped "blob store outside the data store" "FileSystem blob store"

box; export FAKE_BLOB=fail
skipped "blob store setting unreadable" "blob store setting"

# Stalwart not running: the setting can't be read, and the migration must not
# start it.
box; export FAKE_STOPPED=1
skipped "Stalwart not running" "blob store setting"
if grep -q '^start' "$T/systemctl.log"; then echo "FAIL: Stalwart not running: the migration started it"; fail=1; fi

if ((fail)); then
  exit 1
fi
echo "PASS: test_stalwart_migrate_to_v1"

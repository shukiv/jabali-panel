#!/usr/bin/env bash
# install/tests/test_install_module_turns_module_on.sh — `install.sh
# --install-module KEY`, run by an operator, turns the module on in the panel.
#
# It installed the module but left server_settings.<key>_enabled off, and
# `jabali settings set` has no module keys. On a server installed without mail,
# `install.sh --install-module mail` then had Stalwart running while the panel
# treated mail as off: new domains got no Jabali mail. Run by the panel (the
# agent sets JABALI_MODULE_INSTALL_BY_PANEL=1), the flag stays the panel's: it
# set it, and may have cleared it again while the install ran.
#
# Behavioural: runs install_module from install.sh with the per-module installs
# stubbed and a fake mariadb that records what it was asked.
#
# Run from repo root:
#     bash install/tests/test_install_module_turns_module_on.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }
for f in install_module _record_module_enabled; do
  src=$(fn "$f")
  if [[ -z "$src" ]]; then
    echo "FAIL: $f() not found in install.sh"
    fail=1
    continue
  fi
  eval "$src"
done
[[ $fail -eq 0 ]] || exit 1

_ok()   { :; }
_warn() { echo "warn: $*" >>"$tmp/warnings"; }
_die()  { echo "die: $*" >&2; exit 1; }
module_fails=""
for m in quota dns mail security ftp; do
  eval "install_module_$m() { [[ \"\$module_fails\" != $m ]] || _die \"$m install failed\"; }"
done
REPO_DIR="$tmp/repo"
mkdir -p "$REPO_DIR"

mkdir -p "$tmp/bin"
cat >"$tmp/bin/mariadb" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$MARIADB_LOG"
[ -z "$MARIADB_FAILS" ]
EOF
chmod +x "$tmp/bin/mariadb"
PATH="$tmp/bin:$PATH"
export MARIADB_LOG="$tmp/mariadb.log" MARIADB_FAILS=""

# run <key> [env...]: install_module <key>; prints what mariadb was asked (one
# line, or "none"), or "failed" when the install stopped.
run() {
  local key="$1"
  shift
  : >"$MARIADB_LOG"
  if ! (export "$@" 2>/dev/null; install_module "$key") >/dev/null 2>&1; then
    echo failed
    return
  fi
  if [[ -s "$MARIADB_LOG" ]]; then tr '\n' ' ' <"$MARIADB_LOG" | sed 's/ $//'; else echo none; fi
}

expect() { # expect <case> <got> <want>
  if [[ "$2" == "$3" ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1: got '$2', want '$3'"
    fail=1
  fi
}

for m in dns mail quota security ftp; do
  expect "an operator's --install-module $m turns $m on" \
    "$(run "$m" NOOP=1)" "jabali_panel -e UPDATE server_settings SET ${m}_enabled = 1 WHERE id = 1"
done

expect "an install the panel started leaves the flag to the panel" \
  "$(run mail JABALI_MODULE_INSTALL_BY_PANEL=1)" none

module_fails=mail
expect "a failed install turns nothing on" "$(run mail NOOP=1)" failed
module_fails=""

: >"$tmp/warnings"
expect "a database that won't take the flag doesn't fail the install" \
  "$(run mail MARIADB_FAILS=1)" "jabali_panel -e UPDATE server_settings SET mail_enabled = 1 WHERE id = 1"
if grep -q "Server Settings → Modules" "$tmp/warnings"; then
  echo "ok     ... and says to turn it on in Server Settings → Modules"
else
  echo "FAIL   ... without a warning pointing at Server Settings → Modules"
  fail=1
fi

exit $fail

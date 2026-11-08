#!/usr/bin/env bash
# install/tests/test_install_sets_development_channel.sh — an install asked for
# the newest main (JABALI_RELEASE_CHANNEL=development, from bootstrap.sh or the
# classic install.sh) puts the server on the development channel.
#
# The panel's default channel is stable, so a server installed from main would
# otherwise sit ahead of stable on the stable channel. Only that explicit
# request changes the setting: an install without it, or a re-run, leaves the
# channel as it is.
#
# Behavioural: runs set_release_channel_from_install from install.sh against a
# fake panel binary that records what it was asked.
#
# Run from repo root:
#     bash install/tests/test_install_sets_development_channel.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }
src=$(fn set_release_channel_from_install)
if [[ -z "$src" ]]; then
  echo "FAIL: set_release_channel_from_install() not found in install.sh"
  exit 1
fi
eval "$src"

_ok()   { :; }
_warn() { echo "warn: $*" >>"$tmp/warnings"; }

# The fake panel binary records its arguments and fails when $PANEL_FAILS is set.
BIN_PATH="$tmp/jabali-panel"
cat >"$BIN_PATH" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$PANEL_LOG"
[ -z "$PANEL_FAILS" ]
EOF
chmod +x "$BIN_PATH"
export PANEL_LOG="$tmp/panel.log" PANEL_FAILS=""

# run [env...]: set_release_channel_from_install with these variables; prints
# what the panel binary was asked, or "nothing".
run() {
  : >"$PANEL_LOG"
  (
    unset JABALI_RELEASE_CHANNEL
    [[ $# -eq 0 ]] || export "$@"
    set_release_channel_from_install
  )
  if [[ -s "$PANEL_LOG" ]]; then tr '\n' ' ' <"$PANEL_LOG" | sed 's/ $//'; else echo nothing; fi
}

expect() { # expect <case> <got> <want>
  if [[ "$2" == "$3" ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1: got '$2', want '$3'"
    fail=1
  fi
}

expect "JABALI_RELEASE_CHANNEL=development sets the development channel" \
  "$(run JABALI_RELEASE_CHANNEL=development)" "settings set release_channel=development"
expect "an install without it leaves the channel alone" "$(run)" nothing
expect "JABALI_RELEASE_CHANNEL=stable leaves the channel alone" \
  "$(run JABALI_RELEASE_CHANNEL=stable)" nothing

: >"$tmp/warnings"
expect "a panel that won't take the setting doesn't fail the install" \
  "$(run JABALI_RELEASE_CHANNEL=development PANEL_FAILS=1)" "settings set release_channel=development"
if grep -q "Updates page" "$tmp/warnings"; then
  echo "ok     ... and says to set it on the Updates page"
else
  echo "FAIL   ... without a warning pointing at the Updates page"
  fail=1
fi

# main() sets it after the panel binary is installed: before seed_last_built_sha.
main_src=$(fn main)
set_line=$(awk '/^  set_release_channel_from_install$/ {print NR; exit}' <<<"$main_src")
seed_line=$(awk '/^  seed_last_built_sha$/ {print NR; exit}' <<<"$main_src")
if [[ -n "$set_line" && -n "$seed_line" && "$set_line" -lt "$seed_line" ]]; then
  echo "ok   main() sets the channel before seeding the build marker"
else
  echo "FAIL main() doesn't call set_release_channel_from_install before seed_last_built_sha"
  fail=1
fi

exit $fail

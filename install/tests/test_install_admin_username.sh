#!/usr/bin/env bash
# The panel admin's login username: the installer's JABALI_ADMIN_USERNAME (the
# TUI's "Admin login username") when it is a valid username, else the one
# derived from the admin email. seed_admin_env always writes it to panel.env as
# JABALI_BOOTSTRAP_ADMIN_USERNAME, so the panel creates exactly the name the
# final banner shows, and an invalid value never reaches the file.
#
# Run from repo root:
#     bash install/tests/test_install_admin_username.sh
set -uo pipefail

INSTALL_SH="${INSTALL_SH:-install.sh}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

src=""
for fn in _derive_admin_username seed_admin_env; do
  body=$(awk -v f="$fn() {" '$0 == f, /^\}$/' "$INSTALL_SH")
  if [[ -z "$body" ]]; then
    echo "FAIL: $fn not defined in $INSTALL_SH"
    exit 1
  fi
  src+="$body"$'\n'
done
# The seeded marker lives in /etc/jabali on a box; keep the test in $tmp.
src="${src//\/etc\/jabali\/.admin-seeded/$tmp/.admin-seeded}"

# run_seed <email> <username>: a fresh panel.env, then seed_admin_env under the
# installer's shell options. Prints the banner variable.
run_seed() {
  rm -f "$tmp/.admin-seeded"
  : >"$tmp/panel.env"
  (
    set -euo pipefail
    _log() { :; }; _ok() { :; }; _warn() { echo "WARN: $*" >>"$tmp/warn"; }
    eval "$src"
    ENV_FILE="$tmp/panel.env" JABALI_NONINTERACTIVE=1 JABALI_ADMIN_EMAIL="$1" JABALI_ADMIN_USERNAME="$2"
    export ENV_FILE JABALI_NONINTERACTIVE JABALI_ADMIN_EMAIL JABALI_ADMIN_USERNAME
    seed_admin_env
    echo "banner=$JABALI_SEED_USERNAME"
  )
}

check() { # <name> <email> <username> <want>
  rm -f "$tmp/warn"
  local out line
  out=$(run_seed "$2" "$3") || { echo "FAIL: $1: seed_admin_env failed"; fail=1; return; }
  line=$(grep '^JABALI_BOOTSTRAP_ADMIN_USERNAME=' "$tmp/panel.env" || true)
  if [[ "$line" != "JABALI_BOOTSTRAP_ADMIN_USERNAME=$4" || "$out" != "banner=$4" ]]; then
    echo "FAIL: $1: panel.env '$line', $out; want $4"
    fail=1
  fi
  if [[ "$(grep -c '^JABALI_' "$tmp/panel.env")" != 3 ]]; then
    echo "FAIL: $1: panel.env should hold exactly email, username and password:"
    cat "$tmp/panel.env"
    fail=1
  fi
}

check "derived from the email" "Shuki.V@host.example" "" "shuki_v"
check "default email" "admin@host.example" "" "admin"
check "operator's username" "admin@host.example" "ops_lead" "ops_lead"
check "invalid username falls back" "boss@host.example" "Bad Name" "boss"
[[ -s "$tmp/warn" ]] || { echo "FAIL: an invalid username must warn"; fail=1; }
check "newline never reaches panel.env" "boss@host.example" $'ops\nJABALI_INJECTED=1' "boss"
check "too long" "boss@host.example" "$(printf 'a%.0s' {1..33})" "boss"

[[ $fail -eq 0 ]] && echo "OK: admin username seeding"
exit $fail

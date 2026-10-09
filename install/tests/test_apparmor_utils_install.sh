#!/usr/bin/env bash
# install/tests/test_apparmor_utils_install.sh — GH #2001.
#
# install_apparmor installed apparmor + apparmor-utils only when `apparmor`
# itself was missing. Stock Debian ships apparmor without apparmor-utils, so
# aa-complain/aa-enforce never got installed: every jabali profile stayed in
# enforce from its first load, and the Security -> AppArmor mode switch failed
# with "Flip failed". This test runs install_apparmor_packages with dpkg and
# apt-get stubbed and checks which packages it installs.
#
# Run from repo root:
#     bash install/tests/test_apparmor_utils_install.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. install_apparmor installs the packages through the helper. ---
ia_src=$(awk '/^install_apparmor\(\) \{$/,/^\}$/' install.sh)
if ! grep -q '^  install_apparmor_packages$' <<<"$ia_src"; then
  echo "FAIL: install_apparmor does not call install_apparmor_packages"
  fail=1
fi

fn_src=$(awk '/^install_apparmor_packages\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: install_apparmor_packages not defined in install.sh"
  exit 1
fi

# --- 2. Behaviour with dpkg and apt-get stubbed. ---
# run_case <installed packages> <apt-get exit code>
# Prints the apt-get argument lines (one per call), then "rc=<function exit>".
calls=$(mktemp)
trap 'rm -f "$calls"' EXIT

run_case() {
  local installed="$1" apt_rc="$2" rc=0
  : >"$calls"
  (
    INSTALLED=" $installed "
    APT_RC="$apt_rc"
    dpkg() { [[ "$1" == "-s" && "$INSTALLED" == *" $2 "* ]]; }
    # The helper may send apt-get's output to /dev/null, so calls go to a file.
    apt-get() { echo "apt-get $*" >>"$calls"; return "$APT_RC"; }
    _spin() { shift; "$@" || exit $?; }
    _log() { :; }
    _warn() { :; }
    eval "$fn_src"
    install_apparmor_packages
  ) >/dev/null 2>&1 || rc=$?
  cat "$calls"
  echo "rc=$rc"
}

expect() {
  local name="$1" got="$2" want="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $name"
    echo "  want: $want"
    echo "  got:  $got"
    fail=1
  fi
}

APT='apt-get install -y -qq --no-install-recommends'

# The GH #2001 host: apparmor installed, apparmor-utils not.
expect "apparmor installed, apparmor-utils missing: installs apparmor-utils" \
  "$(run_case "apparmor" 0)" \
  "$APT apparmor-utils
rc=0"

expect "both installed: no apt-get call" \
  "$(run_case "apparmor apparmor-utils" 0)" \
  "rc=0"

expect "neither installed: installs both" \
  "$(run_case "" 0)" \
  "$APT apparmor apparmor-utils
rc=0"

# `jabali update` runs this on existing hosts; a failed apparmor-utils install
# must not abort the update.
expect "apparmor-utils install fails: warns and returns 0" \
  "$(run_case "apparmor" 100)" \
  "$APT apparmor-utils
rc=0"

if [[ $fail -ne 0 ]]; then
  exit 1
fi
echo "PASS: install_apparmor_packages installs apparmor-utils when only it is missing (GH #2001)"

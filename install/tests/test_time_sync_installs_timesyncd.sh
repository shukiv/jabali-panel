#!/usr/bin/env bash
# install/tests/test_time_sync_installs_timesyncd.sh — install_time_sync()
# must install systemd-timesyncd when it is missing.
#
# systemd-timesyncd is its own package on current Debian/Ubuntu, and minimal
# cloud images ship without it. install_time_sync() only enabled and started
# the unit, so on such a box every update warned "systemd-timesyncd failed to
# start" and the clock was never synced: a Debian 13 fleet box ran 5 minutes
# slow, past the TOTP window.
#
# Runs the real function body from install.sh with systemctl, apt-get,
# dpkg-query, timedatectl and is_container stubbed, in four cases:
#   1. unit missing, no other time daemon  -> apt-get installs systemd-timesyncd
#   2. unit present                        -> no apt-get
#   3. inside a container                  -> no apt-get (host owns the clock)
#   4. chrony's package installed, stopped -> no apt-get (it would remove chrony)
#
# Run from repo root:
#     bash install/tests/test_time_sync_installs_timesyncd.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

body=$(awk '/^install_time_sync\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if [[ -z "$body" ]]; then
  echo "FAIL: install_time_sync() not found in install.sh"
  exit 1
fi

run_case() { # $1=unit present (0/1) $2=container (0/1) $3=chrony pkg installed (0/1); prints apt-get calls
  (
    UNIT_PRESENT=$1 IN_CONTAINER=$2 CHRONY_PKG=$3
    _log() { :; }
    _ok() { :; }
    _warn() { :; }
    sleep() { :; }
    is_container() { [[ "$IN_CONTAINER" == 1 ]]; }
    systemctl() {
      case "$1" in
        cat) [[ "$UNIT_PRESENT" == 1 ]] ;;
        is-active) return 1 ;;
        *) return 0 ;;
      esac
    }
    timedatectl() { [[ "$1" == show ]] && echo yes; return 0; }
    dpkg-query() { [[ "$CHRONY_PKG" == 1 ]] && echo "install ok installed"; return 0; }
    # The function sends apt-get's output to /dev/null; fd 3 carries the
    # recorded calls out of the subshell.
    apt-get() { echo "apt-get $*" >&3; return 0; }
    eval "$body"
    install_time_sync >/dev/null 2>&1
  ) 3>&1
}

fail=0

out=$(run_case 0 0 0)
if ! grep -q 'install .*systemd-timesyncd' <<<"$out"; then
  echo "FAIL: unit missing and no other time daemon, but systemd-timesyncd was not installed (apt-get calls: ${out:-none})"
  fail=1
fi

out=$(run_case 1 0 0)
if grep -q 'apt-get' <<<"$out"; then
  echo "FAIL: systemd-timesyncd is present but apt-get ran: $out"
  fail=1
fi

out=$(run_case 0 1 0)
if grep -q 'apt-get' <<<"$out"; then
  echo "FAIL: inside a container apt-get ran (the host owns the clock): $out"
  fail=1
fi

out=$(run_case 0 0 1)
if grep -q 'apt-get' <<<"$out"; then
  echo "FAIL: chrony's package is installed but apt-get ran (installing systemd-timesyncd would remove it): $out"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: install_time_sync installs systemd-timesyncd only when it is missing, outside containers, and no other time daemon's package is installed"

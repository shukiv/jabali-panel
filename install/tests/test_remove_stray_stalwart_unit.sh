#!/usr/bin/env bash
# install/tests/test_remove_stray_stalwart_unit.sh — a box without the mail
# module must not keep a jabali-stalwart unit.
#
# `jabali update` from a release before the /opt/stalwart gate copied the unit
# onto every box; the panel then enabled and started it (217/USER crash loop),
# and a start racing `--install-module mail` came up in bootstrap mode.
# remove_stray_stalwart_unit (run by every update) removes it, but only where
# Stalwart is absent.
#
# 1. Functional: the function with getent/systemctl stubbed, paths in a tmpdir.
# 2. Static: provision_new_software calls it.
#
# Run from repo root:
#     bash install/tests/test_remove_stray_stalwart_unit.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
body=$(awk '/^remove_stray_stalwart_unit\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if [[ -z "$body" ]]; then
  echo "FAIL: remove_stray_stalwart_unit() not found in install.sh"
  exit 1
fi

# setup_tree <dir> <opt dir exists 0/1> <unit exists 0/1>
setup_tree() {
  mkdir -p "$1/units/multi-user.target.wants" "$1/units/jabali-stalwart.service.d"
  [[ "$2" == 1 ]] && mkdir -p "$1/opt-stalwart"
  if [[ "$3" == 1 ]]; then
    echo "[Service]" > "$1/units/jabali-stalwart.service"
    ln -s ../jabali-stalwart.service "$1/units/multi-user.target.wants/jabali-stalwart.service"
    echo "[Service]" > "$1/units/jabali-stalwart.service.d/10-jabali-restart.conf"
  fi
  return 0
}

# run_case <dir> <jabali-mail exists 0/1>; prints recorded systemctl calls
run_case() {
  (
    HAVE_USER=$2
    _STALWART_OPT_DIR="$1/opt-stalwart"
    _SYSTEMD_UNIT_DIR="$1/units"
    _ok() { :; }
    getent() { [[ "$HAVE_USER" == 1 ]]; }
    systemctl() { echo "systemctl $*" >&3; }
    eval "$body"
    remove_stray_stalwart_unit >/dev/null 2>&1
  ) 3>&1
}

# A box without mail, with the left-behind unit: everything goes.
T=$(mktemp -d)
setup_tree "$T" 0 1
out=$(run_case "$T" 0)
if [[ -e "$T/units/jabali-stalwart.service" || -e "$T/units/jabali-stalwart.service.d" || -L "$T/units/multi-user.target.wants/jabali-stalwart.service" ]]; then
  echo "FAIL: the stray unit, its drop-ins or its wants link survived on a box without mail"
  fail=1
fi
if ! grep -q 'systemctl disable --now jabali-stalwart.service' <<<"$out"; then
  echo "FAIL: the stray unit was not stopped and disabled (calls: ${out:-none})"
  fail=1
fi
rm -rf "$T"

# Stalwart installed (/opt/stalwart), or its user there (an install under
# way), or no unit at all: nothing is touched.
for c in "1 0 1" "0 1 1" "0 0 0"; do
  read -r opt user unit <<<"$c"
  T=$(mktemp -d)
  setup_tree "$T" "$opt" "$unit"
  out=$(run_case "$T" "$user")
  if [[ -n "$out" ]]; then
    echo "FAIL: case (opt=$opt user=$user unit=$unit) ran systemctl: $out"
    fail=1
  fi
  if [[ "$unit" == 1 && ! -f "$T/units/jabali-stalwart.service" ]]; then
    echo "FAIL: case (opt=$opt user=$user) removed the unit of a host with mail"
    fail=1
  fi
  rm -rf "$T"
done

# Static: every update runs it.
prov=$(awk '/^provision_new_software\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if ! grep -qE '^\s+remove_stray_stalwart_unit$' <<<"$prov"; then
  echo "FAIL: provision_new_software does not call remove_stray_stalwart_unit"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_remove_stray_stalwart_unit"
fi
exit "$fail"

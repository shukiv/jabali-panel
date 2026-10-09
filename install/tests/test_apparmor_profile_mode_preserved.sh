#!/usr/bin/env bash
# install/tests/test_apparmor_profile_mode_preserved.sh — GH #2001.
#
# apply_apparmor_profiles (run by install.sh and on every `jabali update`)
# reloads each jabali AppArmor profile and must keep the mode the profile is
# loaded in: an update never relaxes an enforced profile to complain, and never
# enforces a profile the operator left in complain.
#
# jabali-sendmail moved out of the fpm-exec file into its own file. On the
# first update after that, its new file isn't in /etc/apparmor.d yet, but the
# profile is loaded (from the old fpm-exec file) in jabali-fpm-app's mode. The
# mode must come from the loaded profile, not from whether the file existed.
#
# This test runs apply_apparmor_profiles against the shipped profiles in a temp
# apparmor.d, with aa-status, apparmor_parser, aa-enforce and aa-complain
# stubbed, and checks which mode each profile file ends up in.
#
# Run from repo root:
#     bash install/tests/test_apparmor_profile_mode_preserved.sh
set -euo pipefail

cd "$(dirname "$0")/../.."
REPO_DIR=$PWD

fn_src=$(awk '/^apply_apparmor_profiles\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: apply_apparmor_profiles not defined in install.sh"
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sendmail_file=usr.local.libexec.jabali.jabali-sendmail
[[ -f install/apparmor/$sendmail_file ]] || { echo "FAIL: install/apparmor/$sendmail_file missing"; exit 1; }

# run_case <first_install> <aa-status JSON> <files already in apparmor.d...>
# Prints one "<mode> <file>" line per aa-enforce/aa-complain call, sorted.
run_case() {
  local first="$1" status="$2"
  shift 2
  local aad="$work/apparmor.d" calls="$work/calls"
  rm -rf "$aad"
  mkdir -p "$aad/disable"
  local f
  for f in "$@"; do
    cp "install/apparmor/$f" "$aad/$f"
  done
  printf '%s' "$status" >"$work/status.json"
  : >"$calls"
  (
    STATUS="$work/status.json"
    CALLS="$calls"
    is_container() { return 1; }
    _warn() { :; }
    systemctl() { :; }
    apparmor_parser() { :; }
    install() { cp "${@: -2:1}" "${@: -1}"; }
    aa-status() { cat "$STATUS"; }
    aa-enforce() { echo "enforce $(basename "$1")" >>"$CALLS"; }
    aa-complain() { echo "complain $(basename "$1")" >>"$CALLS"; }
    eval "$fn_src"
    apply_apparmor_profiles "$first" "$aad"
  ) >/dev/null 2>&1
  sort "$calls"
}

# The files a host updated from before the move already has: every shipped
# profile file except the new sendmail one.
mapfile -t existing < <(cd install/apparmor && ls | grep -v -e '\.disabled$' -e "^$sendmail_file$")

# want <name> <got> <file> <mode>: the file was switched to exactly that mode.
fail=0
want() {
  local name="$1" got="$2" file="$3" mode="$4"
  local lines
  lines=$(grep " $file\$" <<<"$got" || true)
  if [[ "$lines" != "$mode $file" ]]; then
    echo "FAIL: $name: $file got [${lines//$'\n'/; }], want [$mode $file]"
    fail=1
  fi
}

all_enforce='{"profiles":{"jabali-panel":"enforce","jabali-bulwark":"enforce","stalwart-mail":"enforce","jabali-fpm-app":"enforce","jabali-sendmail":"enforce"}}'
all_complain='{"profiles":{"jabali-panel":"complain","jabali-bulwark":"complain","stalwart-mail":"complain","jabali-fpm-app":"complain","jabali-sendmail":"complain"}}'
split_modes='{"profiles":{"jabali-panel":"enforce","jabali-bulwark":"enforce","stalwart-mail":"enforce","jabali-fpm-app":"complain","jabali-sendmail":"enforce"}}'

# 1. First update after the move, host with jabali-fpm-app (and so
#    jabali-sendmail) enforced: sendmail stays enforced.
got=$(run_case 0 "$all_enforce" "${existing[@]}")
want "enforced host" "$got" usr.local.libexec.jabali.fpm-exec enforce
want "enforced host" "$got" "$sendmail_file" enforce
want "enforced host" "$got" usr.local.bin.jabali-panel-api enforce

# 2. Same update, host in complain: sendmail stays in complain.
got=$(run_case 0 "$all_complain" "${existing[@]}")
want "complain host" "$got" usr.local.libexec.jabali.fpm-exec complain
want "complain host" "$got" "$sendmail_file" complain

# 3. Later updates, after the admin enforced sendmail and left jabali-fpm-app
#    in complain (both files present): each keeps its own mode.
got=$(run_case 0 "$split_modes" "${existing[@]}" "$sendmail_file")
want "split modes" "$got" usr.local.libexec.jabali.fpm-exec complain
want "split modes" "$got" "$sendmail_file" enforce

# 4. A profile that isn't loaded starts in complain.
got=$(run_case 0 '{"profiles":{"jabali-panel":"enforce"}}' "${existing[@]}")
want "not loaded" "$got" "$sendmail_file" complain
want "not loaded" "$got" usr.local.libexec.jabali.fpm-exec complain
want "not loaded" "$got" usr.local.bin.jabali-panel-api enforce

# 5. First install: everything starts in complain, whatever aa-status says.
got=$(run_case 1 "$all_enforce")
want "first install" "$got" "$sendmail_file" complain
want "first install" "$got" usr.local.libexec.jabali.fpm-exec complain
want "first install" "$got" usr.local.bin.jabali-panel-api complain

if [[ $fail -ne 0 ]]; then
  exit 1
fi
echo "PASS: apply_apparmor_profiles keeps each profile's loaded mode, including jabali-sendmail in its own file"

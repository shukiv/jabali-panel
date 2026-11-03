#!/usr/bin/env bash
# install/tests/test_crowdsec_parsers_on_update.sh — `jabali update` brings
# Jabali's own CrowdSec parsers and acquisition files up to date.
#
# Before this, only a fresh install and the security module install wrote
# them, so a host kept the parser it was installed with. Stalwart 1.0 writes
# auth.failed with its fields in a new order; with the old parser a host that
# moves to 1.0 stops banning mail brute force. This pins:
#   1. provision_new_software (run by every `jabali update`, whatever panel
#      binary drives it) calls converge_crowdsec_jabali_parsers.
#   2. With CrowdSec present, the real installers, run against a temp tree:
#      write a missing tree; replace a stale parser; keep an operator's
#      scenario edit (scenarios are seed-only); reload CrowdSec only when a
#      file changed.
#   3. Without CrowdSec nothing is written.
#   4. A failing installer warns, the other one still runs, and the update
#      goes on (exit 0).
#
# Run from repo root:  bash install/tests/test_crowdsec_parsers_on_update.sh
# Exit 0 = pass.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }

# --- 1. the update path calls it. ---
conv_src="$(fn converge_crowdsec_jabali_parsers)"
if [[ -z "$conv_src" ]]; then
  echo "FAIL: converge_crowdsec_jabali_parsers() not found in install.sh"
  exit 1
fi
if ! grep -qx '  converge_crowdsec_jabali_parsers' <<<"$(fn provision_new_software)"; then
  echo "FAIL: provision_new_software never calls converge_crowdsec_jabali_parsers"
  fail=1
fi

# --- The real installers, with /etc/crowdsec mapped into the temp tree. ---
etc="$tmp/etc/crowdsec"
for f in install_crowdsec_jabali_stalwart_scenarios install_crowdsec_jabali_kratos_scenarios; do
  src="$(fn "$f")"
  if [[ -z "$src" ]]; then
    echo "FAIL: $f() not found in install.sh"
    exit 1
  fi
  eval "$(sed "s|/etc/crowdsec|$etc|g" <<<"$src")"
done
eval "$conv_src"

REPO_DIR="$PWD"
_log() { :; }
_ok() { :; }
_warn() { printf '%s\n' "$*" >>"$tmp/warnings"; }
systemctl() { printf '%s\n' "$*" >>"$tmp/systemctl.log"; }
# The test does not run as root: drop the owner and group flags.
install() {
  local a=()
  while (( $# )); do
    case "$1" in
      -o | -g) shift 2 ;;
      *) a+=("$1"); shift ;;
    esac
  done
  command install "${a[@]}"
}

# PATH holds only the tools the installers use, plus cscli when "installed".
mkdir -p "$tmp/tools" "$tmp/cs"
for t in install cmp basename; do
  ln -s "$(type -P "$t")" "$tmp/tools/$t"
done
printf '#!/bin/sh\nexit 0\n' >"$tmp/cs/cscli"
chmod +x "$tmp/cs/cscli"
saved_path="$PATH"
run() { # run <path>; converge's exit code, PATH restored
  local rc=0
  PATH="$1"
  hash -r
  converge_crowdsec_jabali_parsers || rc=$?
  PATH="$saved_path"
  hash -r
  return "$rc"
}
reloads() { grep -c '^reload crowdsec$' "$tmp/systemctl.log" 2>/dev/null || true; }

parser="$etc/parsers/s01-parse/jabali-stalwart-logs.yaml"
scenario="$etc/scenarios/jabali-stalwart-auth-bf.yaml"
repo_parser="install/crowdsec/stalwart/parsers/s01-parse/jabali-stalwart-logs.yaml"
repo_scenario="install/crowdsec/stalwart/scenarios/jabali-stalwart-auth-bf.yaml"

# --- 3. no CrowdSec: nothing written. ---
if ! run "$tmp/tools"; then
  echo "FAIL: converge exits non-zero on a host without CrowdSec"
  fail=1
fi
if [[ -e "$tmp/etc" ]]; then
  echo "FAIL: converge wrote CrowdSec files on a host without CrowdSec"
  fail=1
fi

# --- 2a. CrowdSec, nothing of ours yet: every file written. ---
run "$tmp/tools:$tmp/cs"
for f in install/crowdsec/stalwart/parsers/s01-parse/jabali-stalwart-logs.yaml \
         install/crowdsec/stalwart/acquis.d/jabali-stalwart.yaml \
         install/crowdsec/panel/parsers/s01-parse/jabali-panel-kratos.yaml \
         install/crowdsec/panel/acquis.d/jabali-panel.yaml \
         "$repo_scenario"; do
  rel="${f#install/crowdsec/*/}"
  if ! cmp -s "$f" "$etc/$rel"; then
    echo "FAIL: first run did not write $etc/$rel"
    fail=1
  fi
done
if [[ "$(reloads)" -lt 1 ]]; then
  echo "FAIL: first run wrote files but did not reload CrowdSec"
  fail=1
fi

# --- 2b. a host on an older parser, with an operator's scenario preset. ---
printf '# an older parser\n' >"$parser"
printf '# operator preset\n' >>"$scenario"
cp "$scenario" "$tmp/preset.yaml"
before="$(reloads)"
run "$tmp/tools:$tmp/cs"
if ! cmp -s "$repo_parser" "$parser"; then
  echo "FAIL: an older Stalwart parser was not replaced"
  fail=1
fi
if ! cmp -s "$tmp/preset.yaml" "$scenario"; then
  echo "FAIL: the operator's scenario preset was overwritten"
  fail=1
fi
if [[ "$(reloads)" -le "$before" ]]; then
  echo "FAIL: the parser changed but CrowdSec was not reloaded"
  fail=1
fi

# --- 2c. nothing changed: no reload. ---
before="$(reloads)"
run "$tmp/tools:$tmp/cs"
if [[ "$(reloads)" -ne "$before" ]]; then
  echo "FAIL: CrowdSec reloaded although no file changed"
  fail=1
fi

# --- 4. a failing installer. ---
install_crowdsec_jabali_stalwart_scenarios() { return 1; }
install_crowdsec_jabali_kratos_scenarios() { printf 'ran\n' >"$tmp/kratos-ran"; }
: >"$tmp/warnings"
if ! run "$tmp/tools:$tmp/cs"; then
  echo "FAIL: a failing parser installer fails the update"
  fail=1
fi
if [[ ! -s "$tmp/warnings" ]]; then
  echo "FAIL: a failing parser installer gives no warning"
  fail=1
fi
if [[ ! -f "$tmp/kratos-ran" ]]; then
  echo "FAIL: the panel parser was skipped after the Stalwart one failed"
  fail=1
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi
echo "PASS: test_crowdsec_parsers_on_update"

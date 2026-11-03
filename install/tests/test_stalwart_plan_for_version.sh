#!/usr/bin/env bash
# install/tests/test_stalwart_plan_for_version.sh
#
# The apply plan template carries Stalwart 1.0's property names. Stalwart 1.0
# renamed one property the plan sets (NetworkListener tlsImplicit ->
# implicitTls), and a 0.16 server rejects the new name, so install_stalwart
# renders the plan through _stalwart_plan_for_version, which gives a 0.x
# server the old name back.
#
# Behavioural: extracts the filter from install.sh and runs the real template
# through it for both versions.
#
# Run from repo root:
#     bash install/tests/test_stalwart_plan_for_version.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmpl=install/stalwart/apply-plan.json.tmpl

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }

eval "$(grep -m1 '^STALWART_VERSION=' install.sh)"
src=$(fn _stalwart_plan_for_version)
if [[ -z "$src" ]]; then
  echo "FAIL: _stalwart_plan_for_version() not found in install.sh"
  exit 1
fi
eval "$src"

# render <version>: the template, as install_stalwart renders it for <version>.
render() { sed -e 's|{{\.MariaDBPassword}}|test-password|g' "$tmpl" | _stalwart_plan_for_version "$1"; }
count() { grep -o "\"$2\"" <<<"$1" | wc -l | tr -d ' '; }
listeners=$(python3 -c '
import json, sys
plan = json.load(open(sys.argv[1]))
print(sum(len(o["value"]) for o in plan if o.get("object") == "x:NetworkListener"))' \
  <(sed -e 's|{{\.MariaDBPassword}}|x|g' "$tmpl"))

if [[ "$(count "$(<"$tmpl")" tlsImplicit)" != 0 ]]; then
  echo "FAIL: the template still uses the 0.16 name tlsImplicit; it carries 1.0's names"
  fail=1
fi

# check <version> <name the listeners must use> <name that must be absent>
check() {
  local version="$1" want="$2" gone="$3" out
  out=$(render "$version")
  if ! python3 -c 'import json, sys; json.loads(sys.stdin.read())' <<<"$out"; then
    echo "FAIL: the plan rendered for Stalwart $version is not valid JSON"
    fail=1
  fi
  if [[ "$(count "$out" "$want")" != "$listeners" ]]; then
    echo "FAIL: for Stalwart $version, $(count "$out" "$want") of $listeners listeners use $want"
    fail=1
  fi
  if [[ "$(count "$out" "$gone")" != 0 ]]; then
    echo "FAIL: the plan rendered for Stalwart $version still names $gone"
    fail=1
  fi
}

check 0.16.27 tlsImplicit implicitTls
check 0.16.0 tlsImplicit implicitTls
check 1.0.2 implicitTls tlsImplicit
check 1.2.0 implicitTls tlsImplicit

# A version that could not be read counts as the pin.
if [[ "$(render unknown)" != "$(render "$STALWART_VERSION")" ]]; then
  echo "FAIL: an unreadable version is not treated as STALWART_VERSION ($STALWART_VERSION)"
  fail=1
fi
if [[ "$(render "")" != "$(render "$STALWART_VERSION")" ]]; then
  echo "FAIL: an empty version is not treated as STALWART_VERSION ($STALWART_VERSION)"
  fail=1
fi

# install_stalwart renders the plan through the filter.
if ! grep -q '| _stalwart_plan_for_version "\$plan_stalwart_version" >"\$stalwart_apply_plan"' <<<"$(fn install_stalwart)"; then
  echo "FAIL: install_stalwart does not render the plan through _stalwart_plan_for_version"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_stalwart_plan_for_version"
fi
exit "$fail"

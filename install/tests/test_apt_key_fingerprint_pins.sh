#!/usr/bin/env bash
# install/tests/test_apt_key_fingerprint_pins.sh — every third-party apt
# signing key install.sh downloads is pinned to a fingerprint before apt is
# told to trust it.
#
# Without a pin, whoever can answer the key URL (a compromised vendor site, CDN
# or mirror) picks which packages the host installs as root. NodeSource,
# Docker and CrowdSec keys were fetched unpinned; Sury and the ondrej/php PPA
# were pinned with a grep that a file carrying the real key plus an appended
# second key would also pass.
#
# Asserts:
#   1. _require_gpg_key_fingerprint, run with real gpg against throwaway keys:
#      accepts the expected key, refuses another key, refuses the expected key
#      with a second key appended, refuses a file that is not a key.
#   2. Each repo's key is checked against its pinned fingerprint, and the check
#      comes before the key reaches the path apt reads it from.
#   3. No apt source trusts a keyring this test does not know about, so a new
#      repo cannot arrive without a pin.
#
# Run from repo root:
#     bash install/tests/test_apt_key_fingerprint_pins.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
pass() { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }

# --- 1. The helper, pulled out of install.sh and run with real gpg. ---
fn_src=$(awk '/^_require_gpg_key_fingerprint\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: _require_gpg_key_fingerprint not defined in install.sh"
  exit 1
fi
eval "$fn_src"
_ok() { :; }
_err() { :; }
_die() { exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GNUPGHOME="$tmp/gnupg"
install -d -m 0700 "$GNUPGHOME"
genkey() {
  gpg --batch --quiet --pinentry-mode loopback --passphrase '' \
    --quick-gen-key "$1" ed25519 sign never 2>/dev/null
  gpg --batch --with-colons --list-keys "$1" 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}'
}
fpr_a=$(genkey "a@example.invalid")
fpr_b=$(genkey "b@example.invalid")
gpg --batch --armor --export "$fpr_a" > "$tmp/a.asc" 2>/dev/null
gpg --batch --export "$fpr_a" > "$tmp/a.gpg" 2>/dev/null
gpg --batch --armor --export "$fpr_a" "$fpr_b" > "$tmp/a_plus_b.asc" 2>/dev/null
printf 'not a key\n' > "$tmp/junk"
unset GNUPGHOME

check() { ( _require_gpg_key_fingerprint "$1" "$2" test ) >/dev/null 2>&1; }

check "$tmp/a.asc" "$fpr_a" && pass "armored key with the pinned fingerprint is accepted" \
  || bad "armored key with the pinned fingerprint was refused"
check "$tmp/a.gpg" "$fpr_a" && pass "binary keyring with the pinned fingerprint is accepted" \
  || bad "binary keyring with the pinned fingerprint was refused"
check "$tmp/a.asc" "$fpr_b" && bad "a key with another fingerprint was accepted" \
  || pass "a key with another fingerprint is refused"
check "$tmp/a_plus_b.asc" "$fpr_a" && bad "the pinned key with a second key appended was accepted" \
  || pass "the pinned key with a second key appended is refused"
check "$tmp/junk" "$fpr_a" && bad "a file that is not a key was accepted" \
  || pass "a file that is not a key is refused"

# --- 2. Each repo checks its pin before the key lands where apt reads it. ---
# fn_body <function name> prints that install.sh function.
fn_body() { awk -v f="$1" '$0 ~ "^"f"\\(\\) \\{$" {on=1} on {print} on && /^\}$/ {exit}' install.sh; }

# expect_pin <label> <body> <fingerprint> <line that installs the keyring>
expect_pin() {
  local label="$1" body="$2" fpr="$3" placed="$4" pin_at placed_at
  pin_at=$(grep -n "_require_gpg_key_fingerprint .*\"$fpr\"" <<<"$body" | head -1 | cut -d: -f1 || true)
  placed_at=$(grep -nF -- "$placed" <<<"$body" | head -1 | cut -d: -f1 || true)
  if [[ -z "$pin_at" ]]; then
    bad "$label: key is not checked against $fpr"
  elif [[ -z "$placed_at" ]]; then
    bad "$label: could not find where the keyring is installed ($placed)"
  elif (( pin_at > placed_at )); then
    bad "$label: the fingerprint is checked after the keyring is installed"
  else
    pass "$label: key pinned to $fpr before it is trusted"
  fi
}

expect_pin "NodeSource" "$(fn_body _install_nodesource_source)" \
  "6F71F525282841EEDAF851B42F59B5F99B1BE0B4" "-o /etc/apt/keyrings/nodesource.gpg"
expect_pin "Docker" "$(fn_body install_docker_engine)" \
  "9DC858229FC7DD38854AE2D88D81803C0EBFCD88" 'mv -f "$docker_key" /etc/apt/keyrings/docker.asc'
expect_pin "CrowdSec" "$(fn_body add_crowdsec_apt_source)" \
  "6A89E3C2303A901A889971D3376ED5326E93CD0C" '--dearmor -o "$keyring"'

sury=$(fn_body _install_sury_source)
for v in SURY_GPG_FINGERPRINT LP_GPG_FINGERPRINT; do
  if grep -q "_require_gpg_key_fingerprint .*\"\$$v\"" <<<"$sury"; then
    pass "Sury: \$$v checked with the strict helper"
  else
    bad "Sury: \$$v is not checked with _require_gpg_key_fingerprint"
  fi
done

# --- 3. No apt source trusts a keyring without a pin. ---
known="/etc/apt/keyrings/crowdsec.gpg
/etc/apt/keyrings/docker.asc
/etc/apt/keyrings/nodesource.gpg
/usr/share/keyrings/sury-php.gpg"
found=$(grep -o -E 'signed-by=[^] ,]+' install.sh | cut -d= -f2 | sort -u)
unknown=$(comm -23 <(printf '%s\n' "$found") <(printf '%s\n' "$known" | sort -u))
if [[ -n "$unknown" ]]; then
  bad "apt sources trust keyrings with no pin in this test: ${unknown//$'\n'/ }"
else
  pass "every signed-by keyring in install.sh is pinned"
fi

exit "$fail"

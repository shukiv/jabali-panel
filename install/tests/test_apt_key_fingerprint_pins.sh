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
#      with a second public or secret key appended, refuses a file that is not
#      a key. _install_pinned_gpg_key writes only the pinned public key, in the
#      format asked for.
#   2. Each repo's key goes through _install_pinned_gpg_key with its pinned
#      fingerprint, and nothing else in the function writes that keyring.
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
for fn in _require_gpg_key_fingerprint _install_pinned_gpg_key; do
  fn_src=$(awk "/^${fn}\\(\\) \\{\$/,/^\\}\$/" install.sh)
  if [[ -z "$fn_src" ]]; then
    echo "FAIL: $fn not defined in install.sh"
    exit 1
  fi
  eval "$fn_src"
done
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
{ cat "$tmp/a.asc"; gpg --batch --pinentry-mode loopback --passphrase '' --armor \
    --export-secret-keys "$fpr_b" 2>/dev/null; } > "$tmp/a_plus_secret_b.asc"
printf 'not a key\n' > "$tmp/junk"
unset GNUPGHOME

# primaries_of <file> lists every top-level key record as <type>:<fingerprint>.
primaries_of() {
  GNUPGHOME="$(mktemp -d -p "$tmp")" gpg --batch --with-colons --show-keys "$1" 2>/dev/null \
    | awk -F: '$1 ~ /^(pub|sec|crt|crs)$/ {p=$1; next} $1=="fpr" && p != "" {print p ":" $10} {p=""}'
}

check() { ( _require_gpg_key_fingerprint "$1" "$2" test ) >/dev/null 2>&1; }

check "$tmp/a.asc" "$fpr_a" && pass "armored key with the pinned fingerprint is accepted" \
  || bad "armored key with the pinned fingerprint was refused"
check "$tmp/a.gpg" "$fpr_a" && pass "binary keyring with the pinned fingerprint is accepted" \
  || bad "binary keyring with the pinned fingerprint was refused"
check "$tmp/a.asc" "$fpr_b" && bad "a key with another fingerprint was accepted" \
  || pass "a key with another fingerprint is refused"
check "$tmp/a_plus_b.asc" "$fpr_a" && bad "the pinned key with a second key appended was accepted" \
  || pass "the pinned key with a second key appended is refused"
check "$tmp/a_plus_secret_b.asc" "$fpr_a" && bad "the pinned key with a secret key appended was accepted" \
  || pass "the pinned key with a secret key appended is refused"
check "$tmp/junk" "$fpr_a" && bad "a file that is not a key was accepted" \
  || pass "a file that is not a key is refused"

install_key() { ( _install_pinned_gpg_key "$1" "$2" test "$3" "$4" ) >/dev/null 2>&1; }

install -d "$tmp/keyrings"
if install_key "$tmp/a.asc" "$fpr_a" "$tmp/keyrings/a.gpg" binary \
   && [[ "$(primaries_of "$tmp/keyrings/a.gpg")" == "pub:$fpr_a" ]] \
   && ! grep -q "BEGIN PGP" "$tmp/keyrings/a.gpg"; then
  pass "binary keyring holds only the pinned public key"
else
  bad "binary keyring was not written as the pinned public key alone"
fi
if install_key "$tmp/a.gpg" "$fpr_a" "$tmp/keyrings/a.asc" armor \
   && [[ "$(primaries_of "$tmp/keyrings/a.asc")" == "pub:$fpr_a" ]] \
   && grep -q "BEGIN PGP PUBLIC KEY BLOCK" "$tmp/keyrings/a.asc"; then
  pass "armored keyring holds only the pinned public key"
else
  bad "armored keyring was not written as the pinned public key alone"
fi
if install_key "$tmp/a_plus_b.asc" "$fpr_a" "$tmp/keyrings/ab.gpg" binary \
   || [[ -e "$tmp/keyrings/ab.gpg" ]]; then
  bad "a refused key still produced a keyring"
else
  pass "a refused key writes no keyring"
fi
if [[ -n "$(find "$tmp/keyrings" -name '.*')" ]]; then
  bad "temp files left beside the keyrings"
else
  pass "no temp files left beside the keyrings"
fi

# --- 2. Each repo installs its key through the pin, and nothing else writes it. ---
# fn_body <function name> prints that install.sh function.
fn_body() { awk -v f="$1" '$0 ~ "^"f"\\(\\) \\{$" {on=1} on {print} on && /^\}$/ {exit}' install.sh; }

# expect_pin <label> <body> <fingerprint or $VAR> <keyring path or $var>
expect_pin() {
  local label="$1" body="$2" fpr="$3" dest="$4" joined
  # Join continuation lines so a call split with "\" reads as one line.
  joined=$(sed -e ':a' -e '/\\$/N; s/\\\n//; ta' <<<"$body")
  if ! grep -qF "_install_pinned_gpg_key " <<<"$joined" \
     || ! grep -F "_install_pinned_gpg_key " <<<"$joined" | grep -qF "\"$fpr\"" \
     || ! grep -F "_install_pinned_gpg_key " <<<"$joined" | grep -F "\"$fpr\"" | grep -qF -- "$dest"; then
    bad "$label: $dest is not installed through _install_pinned_gpg_key with $fpr"
    return
  fi
  if grep -v "_install_pinned_gpg_key " <<<"$joined" | grep -E -- "(-o|>) *\"?$(sed 's/[][\.*^$/]/\\&/g' <<<"$dest")\"?( |$)" >/dev/null; then
    bad "$label: something else in the function writes $dest"
    return
  fi
  pass "$label: $dest holds only the key pinned to $fpr"
}

expect_pin "NodeSource" "$(fn_body _install_nodesource_source)" \
  "6F71F525282841EEDAF851B42F59B5F99B1BE0B4" "/etc/apt/keyrings/nodesource.gpg"
expect_pin "Docker" "$(fn_body install_docker_engine)" \
  "9DC858229FC7DD38854AE2D88D81803C0EBFCD88" "/etc/apt/keyrings/docker.asc"
expect_pin "CrowdSec" "$(fn_body add_crowdsec_apt_source)" \
  "6A89E3C2303A901A889971D3376ED5326E93CD0C" '"$keyring"'
expect_pin "Sury (Debian)" "$(fn_body _install_sury_source)" \
  '$SURY_GPG_FINGERPRINT' "/usr/share/keyrings/sury-php.gpg"
expect_pin "ondrej/php PPA (Ubuntu)" "$(fn_body _install_sury_source)" \
  '$LP_GPG_FINGERPRINT' "/usr/share/keyrings/sury-php.gpg"

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

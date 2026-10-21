#!/usr/bin/env bash
# install/tests/test_apparmor_local_include.sh — every AppArmor profile jabali
# ships includes /etc/apparmor.d/local/<profile file>, so an operator's site
# rules survive `jabali update`.
#
# apply_apparmor_profiles overwrites /etc/apparmor.d/<profile> on every update
# and reloads it. An operator who moved Stalwart's blob store to another disk
# had to allow the new path in the profile itself, and the next update would
# have dropped the rule while Stalwart runs in enforce mode: mail could no
# longer be stored or read. The Debian convention is a local include the
# package never writes.
#
# Asserts, for each install/apparmor/usr.local.* profile:
#   1. Exactly one `include if exists <local/<file name>>`, and it is the last
#      line of the top-level profile block.
#   2. Where apparmor_parser and the system abstractions are present: the
#      profile compiles with no local file, and with one, whose rule then
#      shows up in the preprocessed profile.
# And: install.sh never writes under /etc/apparmor.d/local/.
#
# Run from repo root:
#     bash install/tests/test_apparmor_local_include.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
shopt -s nullglob
profiles=(install/apparmor/usr.local.*)
shopt -u nullglob
if [[ ${#profiles[@]} -eq 0 ]]; then
  echo "FAIL: no profiles under install/apparmor"
  exit 1
fi

have_parser=0
if command -v apparmor_parser >/dev/null 2>&1 && [[ -f /etc/apparmor.d/tunables/global ]]; then
  have_parser=1
fi
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/local"

for profile in "${profiles[@]}"; do
  case "$profile" in *.disabled) continue ;; esac
  name=$(basename "$profile")
  want="  include if exists <local/$name>"

  # --- 1. One include, last line of the top-level profile block. ---
  n=$(grep -cE '^[[:space:]]*include if exists <local/' "$profile" || true)
  if [[ "$n" -ne 1 ]]; then
    echo "FAIL: $name has $n local includes, want 1"
    fail=1
  fi
  block=$(awk '/^profile /{on=1} on{print} on && /^}/{exit}' "$profile")
  last=$(grep -vE '^[[:space:]]*$' <<<"$block" | tail -2 | head -1)
  if [[ "$last" != "$want" ]]; then
    echo "FAIL: $name: the line before the profile's closing brace is '$last', want '$want'"
    fail=1
  fi

  # --- 2. Compiles without and with a local file; the local rule is used. ---
  if [[ $have_parser -eq 1 ]]; then
    if ! out=$(apparmor_parser --skip-kernel-load --skip-cache -I /etc/apparmor.d "$profile" 2>&1); then
      echo "FAIL: $name does not compile with no local file:"
      echo "$out"
      fail=1
    fi
    probe="/srv/jabali-local-include-probe-$name"
    printf '  %s/** r,\n' "$probe" >"$tmpdir/local/$name"
    if ! out=$(apparmor_parser --skip-kernel-load --skip-cache -I "$tmpdir" -I /etc/apparmor.d "$profile" 2>&1); then
      echo "FAIL: $name does not compile with a local file:"
      echo "$out"
      fail=1
    fi
    pre=$(apparmor_parser --skip-kernel-load --skip-cache -I "$tmpdir" -I /etc/apparmor.d -p "$profile" 2>/dev/null || true)
    if ! grep -qF "$probe" <<<"$pre"; then
      echo "FAIL: $name does not include the rule from local/$name"
      fail=1
    fi
  fi
done
if [[ $have_parser -eq 0 ]]; then
  echo "SKIP: apparmor_parser or /etc/apparmor.d/tunables/global missing; compile checks skipped"
fi

# --- 3. The installer leaves the operator's local files alone. ---
if grep -nE '(>|install |cp |mv |tee |rm )[^#]*/etc/apparmor\.d/local/' install.sh; then
  echo "FAIL: install.sh writes under /etc/apparmor.d/local/ — that is the operator's"
  fail=1
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi
echo "PASS: every shipped AppArmor profile includes its local file"

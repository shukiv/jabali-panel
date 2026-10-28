#!/usr/bin/env bash
# install/tests/test_stalwart_no_downgrade.sh — GH #2318.
#
# Two places bring the Stalwart binary to STALWART_VERSION: upgrade_stalwart_binary
# (every `jabali update`) and install_stalwart (fresh install, and again on
# `--install-module mail`). Both compared the installed version for equality
# only, so a server where the operator had installed a NEWER Stalwart by hand
# (1.0 is out, the pin is 0.16) was put back on the pin by the next update. An
# older Stalwart may not read the data a newer one wrote. A newer binary must
# be kept, with a warning; an older, unreadable or missing one still gets the
# pin.
#
# Behavioural: extracts both version checks from install.sh and runs them
# against a fake stalwart binary, with the download stubbed out.
#
# Run from repo root:
#     bash install/tests/test_stalwart_no_downgrade.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

eval "$(grep -m1 '^STALWART_VERSION=' install.sh)"
if [[ ! "${STALWART_VERSION:-}" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "FAIL: STALWART_VERSION not found in install.sh as x.y.z"
  exit 1
fi
maj=${BASH_REMATCH[1]} min=${BASH_REMATCH[2]} pat=${BASH_REMATCH[3]}

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }

# The helper both checks call; absent before the fix.
helper=$(fn _stalwart_refuse_downgrade)
[[ -n "$helper" ]] && eval "$helper"

# upgrade_stalwart_binary, reading the fake binary instead of /usr/local/bin.
upgrade_src=$(fn upgrade_stalwart_binary)
upgrade_src=${upgrade_src//\"\/usr\/local\/bin\/stalwart\"/\"$tmp\/stalwart\"}
if [[ "$upgrade_src" != *"\"$tmp/stalwart\""* ]]; then
  echo "FAIL: upgrade_stalwart_binary no longer reads \"/usr/local/bin/stalwart\"; update this test"
  exit 1
fi
eval "$upgrade_src"

# install_stalwart purges MTAs and creates users, so run only its binary step:
# the `if [[ -x "$stalwart_binary" ]]` block, with its two locals.
install_src=$(fn install_stalwart)
block=$(awk '/^  if \[\[ -x "\$stalwart_binary" \]\]; then$/ {p=1} p {print} p && /^  fi$/ {exit}' <<<"$install_src")
if [[ "$block" != *_install_stalwart_binary* ]]; then
  echo "FAIL: install_stalwart's binary version check not found; update this test"
  exit 1
fi
eval "install_stalwart_binary_step() {
  local stalwart_binary=\"$tmp/stalwart\" stalwart_version=\"\$STALWART_VERSION\"
$block
}"

_log() { :; }
_ok() { :; }
_warn() { printf '%s\n' "$*" >>"$tmp/warnings"; }
_install_stalwart_binary() { printf '%s\n' "$1" >>"$tmp/installed"; }
systemctl() { return 1; }

cat >"$tmp/stalwart.fake" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$FAKE_STALWART_VERSION"
EOF
chmod +x "$tmp/stalwart.fake"

# check <function> <what `stalwart --version` prints, or "absent"> <keep|pin>
check() {
  local f="$1" version="$2" want="$3"
  rm -f "$tmp/installed" "$tmp/warnings" "$tmp/stalwart"
  [[ "$version" != absent ]] && cp "$tmp/stalwart.fake" "$tmp/stalwart"
  if ! FAKE_STALWART_VERSION="$version" "$f"; then
    echo "FAIL: $f with Stalwart $version: returned non-zero"
    fail=1
    return
  fi
  local got=keep
  [[ -s "$tmp/installed" ]] && got="pin $(<"$tmp/installed")"
  case "$want" in
    keep)
      if [[ "$got" != keep ]]; then
        echo "FAIL: $f with Stalwart $version: installed $STALWART_VERSION over it (want: kept)"
        fail=1
      fi ;;
    pin)
      if [[ "$got" != "pin $STALWART_VERSION" ]]; then
        echo "FAIL: $f with Stalwart $version: got '$got' (want: pin $STALWART_VERSION installed)"
        fail=1
      fi ;;
  esac
}

# keep_warns <function> <newer version>: the kept binary is named in a warning.
keep_warns() {
  check "$1" "$2" keep
  if ! grep -qF "$2" "$tmp/warnings" 2>/dev/null; then
    echo "FAIL: $1 kept Stalwart $2 without a warning naming it"
    fail=1
  fi
}

for f in upgrade_stalwart_binary install_stalwart_binary_step; do
  keep_warns "$f" "$((maj + 1)).0.0"
  keep_warns "$f" "$maj.$min.$((pat + 1))"
  check "$f" "$STALWART_VERSION" keep
  check "$f" "absent" pin
  check "$f" "garbage" pin
  ((pat > 0)) && check "$f" "$maj.$min.$((pat - 1))" pin
  # Older, but larger as a string: the parts compare as numbers.
  ((min >= 10)) && check "$f" "$maj.9.99" pin
done

if ((fail)); then
  exit 1
fi
echo "PASS: a newer Stalwart is kept with a warning; an older, unreadable or missing one gets $STALWART_VERSION"

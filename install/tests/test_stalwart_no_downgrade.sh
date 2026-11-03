#!/usr/bin/env bash
# install/tests/test_stalwart_no_downgrade.sh — GH #2318, ADR-0179.
#
# Two places bring the Stalwart binary to STALWART_VERSION: upgrade_stalwart_binary
# (every `jabali update`) and install_stalwart (fresh install, and again on
# `--install-module mail`). Both compared the installed version for equality
# only, so a server where the operator had installed a NEWER Stalwart by hand
# was put back on the pin by the next update. An older Stalwart may not read
# the data a newer one wrote. A newer binary must be kept, with a warning; an
# older 1.x, unreadable or missing one still gets the pin.
#
# Since the pin is 1.x, a 0.16 store must never meet a 1.x binary (1.0 cannot
# open it): a 0.x binary at STALWART_SOURCE_VERSION is migrated by
# upgrade_stalwart_binary only (migrate_stalwart_to_v1), an older 0.x one is
# first brought to the source version, and install_stalwart never migrates.
# With no binary to read, the store decides: empty or stamped 1 gets the pin,
# unstamped data (written before 1.0) gets the source version.
#
# Behavioural: extracts both version checks from install.sh and runs them
# against a fake stalwart binary, with the download and the migration stubbed.
#
# Run from repo root:
#     bash install/tests/test_stalwart_no_downgrade.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

eval "$(grep -m1 '^STALWART_VERSION=' install.sh)"
eval "$(grep -m1 '^STALWART_SOURCE_VERSION=' install.sh)"
if [[ ! "${STALWART_VERSION:-}" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "FAIL: STALWART_VERSION not found in install.sh as x.y.z"
  exit 1
fi
maj=${BASH_REMATCH[1]} min=${BASH_REMATCH[2]} pat=${BASH_REMATCH[3]}
if [[ ! "${STALWART_SOURCE_VERSION:-}" =~ ^0\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "FAIL: STALWART_SOURCE_VERSION not found in install.sh as 0.y.z"
  exit 1
fi
smin=${BASH_REMATCH[1]} spat=${BASH_REMATCH[2]}

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }

for f in _stalwart_refuse_downgrade _stalwart_store_major _stalwart_mark_store_v1 \
         _stalwart_next_step _stalwart_installed_version _stalwart_restart_if_active \
         _install_stalwart_binary_step_for _stalwart_migrate_status upgrade_stalwart_binary; do
  src=$(fn "$f")
  if [[ -z "$src" ]]; then
    echo "FAIL: $f() not found in install.sh"
    exit 1
  fi
  eval "$src"
done

# install_stalwart purges MTAs and creates users, so run only its binary step:
# the `if [[ -x "$stalwart_binary" ]]` block, with its two locals.
install_src=$(fn install_stalwart)
block=$(awk '/^  if \[\[ -x "\$stalwart_binary" \]\]; then$/ {p=1} p {print} p && /^  fi$/ {exit}' <<<"$install_src")
if [[ "$block" != *_install_stalwart_binary_step_for* ]]; then
  echo "FAIL: install_stalwart's binary version check not found; update this test"
  exit 1
fi
eval "install_stalwart_binary_step() {
  local stalwart_binary=\"\$STALWART_BIN_LINK\" stalwart_version=\"\$STALWART_VERSION\"
$block
}"

STALWART_BIN_LINK="$tmp/stalwart"
STALWART_STORE_DIR="$tmp/store"
STALWART_STORE_STAMP="$tmp/stamp"
REPO_DIR="$tmp/repo"
_log() { :; }
_ok() { :; }
_warn() { printf '%s\n' "$*" >>"$tmp/warnings"; }
_install_stalwart_binary() { printf '%s\n' "$1" >>"$tmp/installed"; }
migrate_stalwart_to_v1() { printf 'migrate\n' >>"$tmp/installed"; return "${FAKE_MIGRATE_RC:-0}"; }
systemctl() { return 1; }

cat >"$tmp/stalwart.fake" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$FAKE_STALWART_VERSION"
EOF
chmod +x "$tmp/stalwart.fake"

# check <function> <what `stalwart --version` prints, or "absent"> <store> <want>
#   store: empty | v0 (data, no stamp) | v1 (data, stamp 1)
#   want:  what was installed and run, in order, space-separated ("keep" = nothing)
check() {
  local f="$1" version="$2" store="$3" want="$4"
  rm -rf "$tmp/installed" "$tmp/warnings" "$tmp/stalwart" "$tmp/store" "$tmp/stamp"
  mkdir -p "$tmp/store"
  case "$store" in
    v0) touch "$tmp/store/CURRENT" ;;
    v1) touch "$tmp/store/CURRENT"; printf '1\n' >"$tmp/stamp" ;;
  esac
  [[ "$version" != absent ]] && cp "$tmp/stalwart.fake" "$tmp/stalwart"
  if ! FAKE_STALWART_VERSION="$version" "$f"; then
    echo "FAIL: $f with Stalwart $version ($store store): returned non-zero"
    fail=1
    return
  fi
  local got=keep
  [[ -s "$tmp/installed" ]] && got="$(paste -sd' ' "$tmp/installed")"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $f with Stalwart $version ($store store): got '$got', want '$want'"
    fail=1
  fi
}

# keep_warns <function> <newer version>: the kept binary is named in a warning.
keep_warns() {
  check "$1" "$2" v1 keep
  if ! grep -qF "$2" "$tmp/warnings" 2>/dev/null; then
    echo "FAIL: $1 kept Stalwart $2 without a warning naming it"
    fail=1
  fi
}

# stamped <function> <version> <store> <want>: as check, and afterwards the
# store is stamped as a 1.x store.
stamped() {
  check "$1" "$2" "$3" "$4"
  if [[ "$(cat "$tmp/stamp" 2>/dev/null)" != 1 ]]; then
    echo "FAIL: $1 with Stalwart $2 ($3 store): the store was not stamped as a 1.x store"
    fail=1
  fi
}

old_source="0.$smin.$((spat - 1))"
for f in upgrade_stalwart_binary install_stalwart_binary_step; do
  keep_warns "$f" "$((maj + 1)).0.0"
  keep_warns "$f" "$maj.$min.$((pat + 1))"
  stamped "$f" "$STALWART_VERSION" v1 keep
  stamped "$f" absent empty "$STALWART_VERSION"
  check "$f" garbage empty "$STALWART_VERSION"
  check "$f" absent v1 "$STALWART_VERSION"
  check "$f" garbage v1 "$STALWART_VERSION"
  ((pat > 0)) && stamped "$f" "$maj.$min.$((pat - 1))" v1 "$STALWART_VERSION"
  # Older, but larger as a string: the parts compare as numbers.
  ((min >= 10)) && check "$f" "$maj.9.99" v1 "$STALWART_VERSION"
done

# A 0.16 store never gets a 1.x binary. Only `jabali update` migrates it.
check upgrade_stalwart_binary "$STALWART_SOURCE_VERSION" v0 migrate
check upgrade_stalwart_binary "$old_source" v0 "$STALWART_SOURCE_VERSION migrate"
check upgrade_stalwart_binary "0.9.99" v0 "$STALWART_SOURCE_VERSION migrate"
check upgrade_stalwart_binary absent v0 "$STALWART_SOURCE_VERSION migrate"
check upgrade_stalwart_binary garbage v0 "$STALWART_SOURCE_VERSION migrate"
check install_stalwart_binary_step "$STALWART_SOURCE_VERSION" v0 keep
grep -q "next 'jabali update'" "$tmp/warnings" 2>/dev/null \
  || { echo "FAIL: install_stalwart kept 0.16 without saying the next update moves it"; fail=1; }
check install_stalwart_binary_step "$old_source" v0 "$STALWART_SOURCE_VERSION"
check install_stalwart_binary_step absent v0 "$STALWART_SOURCE_VERSION"
check install_stalwart_binary_step garbage v0 "$STALWART_SOURCE_VERSION"
if [[ -f "$tmp/stamp" ]]; then
  echo "FAIL: a 0.16 store was stamped as a 1.x store"
  fail=1
fi

# The update step's result: a migration skipped by its checks (2: it said why,
# nothing changed) is not an update failure; a failed one (1) is.
FAKE_MIGRATE_RC=2 check upgrade_stalwart_binary "$STALWART_SOURCE_VERSION" v0 migrate
rc=0; FAKE_STALWART_VERSION="$STALWART_SOURCE_VERSION" FAKE_MIGRATE_RC=1 upgrade_stalwart_binary || rc=$?
[[ "$rc" != 0 ]] || { echo "FAIL: a failed migration returned success from upgrade_stalwart_binary"; fail=1; }

if ((fail)); then
  exit 1
fi
echo "PASS: a newer Stalwart is kept with a warning; an older 1.x, unreadable or missing one gets $STALWART_VERSION; a 0.16 store is only migrated, by jabali update"

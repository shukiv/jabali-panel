#!/usr/bin/env bash
# install/tests/test_install_composer_without_home.sh — Composer installs when
# install.sh runs without HOME, and an update brings it back.
#
# Composer's installer refuses to run without HOME or COMPOSER_HOME. A fresh
# install started by systemd-run has no HOME, so composer-latest and
# composer-lts both failed, the dispatcher was never installed, and the box
# had no `composer` at all; only the lts failure printed a warning. The step
# ran only from main(), so `jabali update` never retried it.
#
# 1. ensure_composer_versions, run with HOME and COMPOSER_HOME unset, still
#    installs both phars and the dispatcher. A fake php stands in for the
#    installer and refuses to run without HOME, as Composer's does.
# 2. A failed composer-latest install is reported.
# 3. With both phars present it downloads nothing.
# 4. Source-level: install_base_packages and provision_new_software (the
#    update path) both call it.
#
# Run from repo root:
#     bash install/tests/test_install_composer_without_home.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn() { awk -v n="$1" '$0 == n "() {" {p=1} p {print} p && /^}$/ {exit}' install.sh; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn ensure_composer_versions > "$tmp/fn.sh"
if [[ ! -s "$tmp/fn.sh" ]]; then
  echo "FAIL: ensure_composer_versions not defined in install.sh"
  exit 1
fi

cat > "$tmp/php-fake" <<'PHP'
#!/usr/bin/env bash
echo "HOME=${HOME-<unset>}" >> "$FAKE_PHP_LOG"
if [[ -z "${HOME:-}" && -z "${COMPOSER_HOME:-}" ]]; then
  echo "The HOME or COMPOSER_HOME environment variable must be set for composer to run correctly"
  exit 1
fi
dir="" name=""
for a in "$@"; do
  case "$a" in
    --install-dir=*) dir="${a#--install-dir=}" ;;
    --filename=*) name="${a#--filename=}" ;;
  esac
done
[[ "${FAKE_PHP_FAIL:-}" == "$name" ]] && exit 1
printf '#!/usr/bin/env php\n' > "$dir/$name" && chmod 0755 "$dir/$name"
PHP
chmod 0755 "$tmp/php-fake"

# Stubs for install.sh's helpers. The download serves a fake installer whose
# signature matches; `install` puts /etc/jabali-panel under $tmp.
cat > "$tmp/stubs.sh" <<'STUBS'
_log()  { echo "[i] $*"; }
_ok()   { echo "[ok] $*"; }
_warn() { echo "[!] $*"; }
_err()  { echo "[x] $*"; }
curl_ipv4_retry() {
  local out="" url=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -o) out="$2"; shift 2 ;;
      -*) shift ;;
      *) url="$1"; shift ;;
    esac
  done
  case "$url" in
    */installer) printf 'fake installer\n' > "$out" ;;
    */installer.sig) printf 'fake installer\n' | sha384sum | awk '{print $1}' ;;
    *) return 1 ;;
  esac
}
install() {
  local a args=()
  for a in "$@"; do args+=("${a/#\/etc\/jabali-panel/$FAKE_ETC}"); done
  command install "${args[@]}"
}
STUBS

# run BIN_DIR [PHAR_TO_FAIL] — ensure_composer_versions with no HOME.
run() {
  mkdir -p "$1"
  env -u HOME -u COMPOSER_HOME \
    FAKE_PHP_LOG="$tmp/php.log" FAKE_PHP_FAIL="${2:-}" FAKE_ETC="$tmp/etc" \
    bash -c 'source "$1"; source "$2"; ensure_composer_versions "$3" "$4"' \
    _ "$tmp/stubs.sh" "$tmp/fn.sh" "$tmp/php-fake" "$1" 2>&1
}

# --- 1. No HOME: both phars and the dispatcher are installed. ---
: > "$tmp/php.log"
out=$(run "$tmp/bin")
for f in composer-latest composer-lts composer; do
  if [[ ! -x "$tmp/bin/$f" ]]; then
    echo "FAIL: without HOME, $f was not installed"
    echo "$out" | sed 's/^/    /'
    fail=1
  fi
done
if grep -q 'HOME=<unset>' "$tmp/php.log"; then
  echo "FAIL: the Composer installer ran without HOME"
  fail=1
fi

# --- 2. A failed composer-latest install is reported. ---
out=$(run "$tmp/bin2" composer-latest)
if ! grep -q 'composer-latest install failed' <<<"$out"; then
  echo "FAIL: a failed composer-latest install printed no warning"
  echo "$out" | sed 's/^/    /'
  fail=1
fi
if [[ -e "$tmp/bin2/composer" ]]; then
  echo "FAIL: the dispatcher was installed without composer-latest"
  fail=1
fi

# --- 3. Both phars present: nothing is downloaded. ---
: > "$tmp/php.log"
run "$tmp/bin" >/dev/null
if [[ -s "$tmp/php.log" ]]; then
  echo "FAIL: Composer was reinstalled although both versions were present"
  fail=1
fi

# --- 4. Fresh install and update both call it. ---
# Captured first: `fn | grep -q` under pipefail fails when grep exits early
# and awk, still printing a long function, takes SIGPIPE.
body=$(fn install_base_packages)
if ! grep -qx '  ensure_composer_versions "php${primary_version}"' <<<"$body"; then
  echo "FAIL: install_base_packages() does not call ensure_composer_versions"
  fail=1
fi
body=$(fn provision_new_software)
if ! grep -q 'ensure_composer_versions "$_upd_php_bin"' <<<"$body"; then
  echo "FAIL: provision_new_software() does not call ensure_composer_versions"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: Composer installs without HOME, and on update"
fi
exit "$fail"

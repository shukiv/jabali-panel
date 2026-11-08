#!/usr/bin/env bash
# install/tests/test_bootstrap_installs_stable.sh — `curl get.jabali-panel.com |
# bash` installs the stable release.
#
# bootstrap.sh used to install the newest main build (releases/latest), so a
# new server ran a build ahead of `stable` while following the stable channel,
# and `jabali update` kept it there until the next stable release passed it. It
# now reads the commit the `stable` tag names from <repo>.git/info/refs, installs
# release-<short>, checks the tarball's MANIFEST names that commit, and passes
# the channel on. JABALI_RELEASE_CHANNEL=development keeps the newest build.
#
# Two parts: stable_commit and release_for_commit run here directly; then the
# real bootstrap.sh runs end to end against a fake curl serving local files,
# as root in a user namespace (`unshare -r`). Where user namespaces are not
# allowed, that part is skipped and says so.
#
# Run from repo root:
#     bash install/tests/test_bootstrap_installs_stable.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

expect() { # expect <case> <got> <want>
  if [[ "$2" == "$3" ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1: got '$2', want '$3'"
    fail=1
  fi
}

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' bootstrap.sh; }
for f in stable_commit release_for_commit; do
  src=$(fn "$f")
  if [[ -z "$src" ]]; then
    echo "FAIL: $f() not found in bootstrap.sh"
    exit 1
  fi
  eval "$src"
done

stable=1111111111111111111111111111111111111111
tagobj=2222222222222222222222222222222222222222
latest=3333333333333333333333333333333333333333
rc=4444444444444444444444444444444444444444

# refs_file <file> <lines...>: a git ref list as GitHub serves it, one pkt-line
# per ref; the first carries the capabilities after a NUL.
refs_file() {
  local out="$1" line
  shift
  {
    printf '001e# service=git-upload-pack\n0000'
    printf '%04x%s HEAD\0multi_ack side-band-64k\n' $((4 + 40 + 6 + 1 + 26)) "$latest"
    for line in "$@"; do
      printf '%04x%s\n' $((4 + ${#line} + 1)) "$line"
    done
    printf '0000'
  } >"$out"
}

# --- stable_commit ------------------------------------------------------------
refs_file "$tmp/r1" "$latest refs/heads/main" "$rc refs/tags/stable-rc" "$stable refs/tags/stable" "$latest refs/tags/release-333333333"
expect "a lightweight stable tag names its commit" "$(stable_commit "$tmp/r1")" "$stable"

refs_file "$tmp/r2" "$latest refs/heads/main" "$tagobj refs/tags/stable" "$stable refs/tags/stable^{}"
expect "an annotated stable tag: the peeled line names the commit" "$(stable_commit "$tmp/r2")" "$stable"

refs_file "$tmp/r3" "$latest refs/heads/main" "$rc refs/tags/stable-rc" "$rc refs/tags/old-stable"
expect "no stable tag (only look-alikes)" "$(stable_commit "$tmp/r3")" ""

# --- release_for_commit (fake curl: HEAD on a URL succeeds when the file exists)
web="$tmp/web"
REPO_WEB="https://example.test/o/r"
curl() {
  local a url=""
  for a in "$@"; do [[ "$a" == https://* ]] && url="$a"; done
  [[ -e "$web/${url#https://}" ]]
}
put_release() { # put_release <commit> <short length>
  local short="${1:0:$2}" d
  d="$web/example.test/o/r/releases/download/release-$short"
  mkdir -p "$d"
  : >"$d/jabali-release-$short.tar.gz"
  : >"$d/jabali-release-$short.tar.gz.sha256"
}
put_release "$stable" 9
tar_url="" sum_url=""
release_for_commit "$stable" || true
expect "release-<9 hex> is found" "$tar_url" "https://example.test/o/r/releases/download/release-111111111/jabali-release-111111111.tar.gz"
expect "  ... with its sidecar" "$sum_url" "${tar_url}.sha256"

put_release "$rc" 10
tar_url=""
release_for_commit "$rc" || true
expect "a release named with a 10-hex short commit is found" "$tar_url" "https://example.test/o/r/releases/download/release-4444444444/jabali-release-4444444444.tar.gz"

tar_url=""
if release_for_commit "$tagobj"; then got=found; else got=none; fi
expect "no release for the commit" "$got/$tar_url" "none/"
unset -f curl

# --- the real bootstrap.sh, end to end -------------------------------------------
if ! unshare -r true 2>/dev/null; then
  echo "SKIP end-to-end: user namespaces are not available here (unshare -r)"
  exit $fail
fi

# Release tarballs: a fake installer that records what it was started with.
make_release() { # make_release <commit> <short>
  local short="$2" b="$tmp/build-$2" d
  mkdir -p "$b/bin"
  cat >"$b/bin/jabali-installer" <<'EOF'
#!/bin/bash
{
  echo "channel=${JABALI_RELEASE_CHANNEL:-unset}"
  echo "built_from=$(awk -F= '/^full_sha=/{print $2}' "$JABALI_RELEASE_MANIFEST")"
} >"$INSTALLER_RECORD"
EOF
  chmod +x "$b/bin/jabali-installer"
  : >"$b/install.sh"
  printf 'short_sha=%s\nfull_sha=%s\n' "$short" "$1" >"$b/MANIFEST"
  d="$web/github.com/shukiv/jabali-panel/releases/download/release-$short"
  mkdir -p "$d"
  # Owned by root, as a release is: root in the user namespace can't chown to
  # an unmapped uid.
  tar -C "$b" --owner=0 --group=0 --numeric-owner -czf "$d/jabali-release-$short.tar.gz" bin install.sh MANIFEST
  (cd "$d" && sha256sum "jabali-release-$short.tar.gz" >"jabali-release-$short.tar.gz.sha256")
}
rm -rf "$web"
make_release "$stable" 111111111
make_release "$latest" 333333333
latest_dir="$web/github.com/shukiv/jabali-panel/releases/latest/download"
mkdir -p "$latest_dir"
cp "$web/github.com/shukiv/jabali-panel/releases/download/release-333333333/jabali-release-333333333.tar.gz" "$latest_dir/jabali-release.tar.gz"
cp "$web/github.com/shukiv/jabali-panel/releases/download/release-333333333/jabali-release-333333333.tar.gz.sha256" "$latest_dir/jabali-release.tar.gz.sha256"
refs_dir="$web/github.com/shukiv"
refs_file "$tmp/refs-stable" "$latest refs/heads/main" "$stable refs/tags/stable"
refs_file "$tmp/refs-none" "$latest refs/heads/main"

# A curl that serves $FAKE_WEB: the URL's host and path are a file under it
# (the ref list's query string dropped). -I checks it exists; otherwise its
# body goes to -o or stdout. Missing: exit 22, as curl -f does on a 404.
mkdir -p "$tmp/fakebin"
cat >"$tmp/fakebin/curl" <<'EOF'
#!/bin/bash
url="" out="" head=0
while (( $# )); do
  case "$1" in
    -o) out="$2"; shift ;;
    -I|-fsIL|-fsI) head=1 ;;
    --connect-timeout|--retry|--retry-delay|-H|-w) shift ;;
    https://*) url="$1" ;;
  esac
  shift
done
f="$FAKE_WEB/${url#https://}"
f="${f%%\?*}"
[[ -f "$f" ]] || exit 22
(( head )) && exit 0
if [[ -n "$out" ]]; then cp "$f" "$out"; else cat "$f"; fi
EOF
chmod +x "$tmp/fakebin/curl"

# run_bootstrap <refs file> [env...]: prints what the installer was started
# with, or "refused".
run_bootstrap() {
  local refs="$1"
  shift
  cp "$refs" "$refs_dir/jabali-panel.git/info/refs"
  : >"$tmp/record"
  if env PATH="$tmp/fakebin:$PATH" FAKE_WEB="$web" INSTALLER_RECORD="$tmp/record" "$@" \
       unshare -r bash bootstrap.sh </dev/null >"$tmp/out" 2>&1; then
    tr '\n' ' ' <"$tmp/record" | sed 's/ $//'
  else
    [[ -z "${DEBUG:-}" ]] || sed 's/^/  bootstrap: /' "$tmp/out" >&2
    echo refused
  fi
}
mkdir -p "$refs_dir/jabali-panel.git/info"

expect "default: the stable release, channel passed on" \
  "$(run_bootstrap "$tmp/refs-stable")" "channel=stable built_from=$stable"
expect "JABALI_RELEASE_CHANNEL=development: the newest build" \
  "$(run_bootstrap "$tmp/refs-stable" JABALI_RELEASE_CHANNEL=development)" "channel=development built_from=$latest"
expect "no stable tag: refused, not the newest build instead" \
  "$(run_bootstrap "$tmp/refs-none")" refused
expect "an unknown channel is refused" \
  "$(run_bootstrap "$tmp/refs-stable" JABALI_RELEASE_CHANNEL=beta)" refused

# The stable release's tarball built from another commit: refused.
cp "$web/github.com/shukiv/jabali-panel/releases/download/release-333333333/jabali-release-333333333.tar.gz" \
   "$web/github.com/shukiv/jabali-panel/releases/download/release-111111111/jabali-release-111111111.tar.gz"
(cd "$web/github.com/shukiv/jabali-panel/releases/download/release-111111111" \
  && sha256sum jabali-release-111111111.tar.gz >jabali-release-111111111.tar.gz.sha256)
expect "a stable tarball whose MANIFEST names another commit is refused" \
  "$(run_bootstrap "$tmp/refs-stable")" refused

exit $fail

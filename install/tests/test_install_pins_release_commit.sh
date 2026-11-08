#!/usr/bin/env bash
# install/tests/test_install_pins_release_commit.sh — a prebuilt release is
# installed from the commit it was built from.
#
# bootstrap.sh hands install.sh the release's prebuilt binaries and its
# MANIFEST. clone_or_update_repo used to check out the newest main whatever the
# release was, so a server got one commit's binaries next to another commit's
# install/ payloads, units and templates, and `jabali update` then compared the
# stable tag with main's HEAD. It now checks out MANIFEST full_sha, fetching it
# when it is not on main (a stable hotfix), and refuses a release older than the
# server, before the checkout moves: older than the panel database's schema, or
# than the checkout already there.
#
# Behavioural: runs the real clone_or_update_repo against a local git remote.
# Only sudo, DNS, MariaDB, the root-ownership check and the chown are stubbed.
#
# Run from repo root:
#     bash install/tests/test_install_pins_release_commit.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }

for f in root_git clone_or_update_repo _prebuilt_commit _fetch_release_commit \
         _max_migration_at _live_schema_version _refuse_older_release; do
  src=$(fn "$f")
  if [[ -z "$src" ]]; then
    echo "FAIL: $f() not found in install.sh"
    fail=1
    continue
  fi
  eval "$src"
done
[[ $fail -eq 0 ]] || exit 1

_log()  { echo "log: $*" >&2; }
_warn() { echo "warn: $*" >&2; }
_ok()   { echo "ok: $*" >&2; }
_die()  { echo "die: $*" >&2; exit 1; }
root_repo_layout() { :; }
getent() { return 0; }
# The test runs unprivileged: report the checkout as root-owned.
stat() { if [[ "$1" == "-c" && "$2" == "%u" ]]; then echo 0; else command stat "$@"; fi; }
mkdir -p "$tmp/bin"
printf '#!/bin/sh\nexit 0\n' >"$tmp/bin/sudo"
# The panel database's schema version comes from $FAKE_SCHEMA_FILE: empty is
# a server with no panel database yet.
cat >"$tmp/bin/mariadb" <<'EOF'
#!/bin/sh
s=$(cat "$FAKE_SCHEMA_FILE" 2>/dev/null)
case "$*" in
  *information_schema*) if [ -n "$s" ]; then echo 1; else echo 0; fi ;;
  *schema_migrations*) echo "$s" ;;
esac
EOF
chmod +x "$tmp/bin/sudo" "$tmp/bin/mariadb"
PATH="$tmp/bin:$PATH"
export FAKE_SCHEMA_FILE="$tmp/schema"
schema() { printf '%s' "$1" >"$FAKE_SCHEMA_FILE"; }
schema ""

prebuilt=0
_prebuilt_ready() { [[ $prebuilt -eq 1 ]]; }

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.test GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.test
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null

remote="$tmp/remote"
git init -q -b main "$remote"
mig="$remote/panel-api/internal/db/migrations"
mkdir -p "$mig"
commit_with() { # commit_with <migration number> -> prints the commit
  : >"$mig/$(printf '%06d' "$1")_m$1.up.sql"
  git -C "$remote" add -A
  git -C "$remote" commit -q -m "m$1"
  git -C "$remote" rev-parse HEAD
}
c1=$(commit_with 1)
c2=$(commit_with 2)
c3=$(commit_with 3)

REPO_URL="file://$remote"
REPO_BRANCH=main
REPO_TOKEN=""
LOG_FILE=/dev/null
JABALI_RELEASE_MANIFEST="$tmp/MANIFEST"

manifest() { printf 'short_sha=%s\nfull_sha=%s\nrelease_format=v1\n' "${1:0:9}" "$1" >"$JABALI_RELEASE_MANIFEST"; }

# checkout_at <dir> <commit>: an installed server's checkout, on main at commit.
checkout_at() {
  git clone -q --branch main "$REPO_URL" "$1"
  git -C "$1" reset -q --hard "$2"
}

# run_install <dir>: clone_or_update_repo for <dir>; prints the checkout's HEAD,
# or "refused" when the install stopped.
run_install() {
  local out
  if out=$( (REPO_DIR="$1"; clone_or_update_repo) 2>&1 ); then
    git -C "$1" rev-parse HEAD
  else
    printf '%s\n' "$out" >"$1.log"
    echo refused
  fi
}

expect() { # expect <case> <got> <want>
  if [[ "$2" == "$3" ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1: got ${2:0:9}, want ${3:0:9}"
    fail=1
  fi
}

# --- fresh installs ----------------------------------------------------------
prebuilt=1
manifest "$c2"
expect "fresh install of a release older than main gets the release's commit" \
  "$(run_install "$tmp/fresh-older")" "$c2"

prebuilt=0
expect "fresh source install (no prebuilt release) still gets the newest main" \
  "$(run_install "$tmp/fresh-source")" "$c3"

prebuilt=1
printf 'short_sha=%s\nrelease_format=v1\n' "${c2:0:9}" >"$JABALI_RELEASE_MANIFEST"
expect "a MANIFEST without full_sha leaves the checkout on main" \
  "$(run_install "$tmp/fresh-no-full-sha")" "$c3"

printf 'full_sha=%s;x\n' "$c2" >"$JABALI_RELEASE_MANIFEST"
expect "a malformed full_sha is not used" \
  "$(run_install "$tmp/fresh-bad-full-sha")" "$c3"

# --- a fresh clone on a server whose database is already there --------------
# (its checkout was moved aside as not root-owned, or deleted)
manifest "$c2"
schema 2
expect "fresh clone, database at the release's migrations" \
  "$(run_install "$tmp/fresh-db-same")" "$c2"
schema 3
expect "fresh clone, database at a newer migration than the release: refused" \
  "$(run_install "$tmp/fresh-db-newer")" refused
schema "dirty?"
expect "fresh clone, database schema version unreadable: refused" \
  "$(run_install "$tmp/fresh-db-unreadable")" refused
schema ""

# --- re-runs on an installed server -------------------------------------------
manifest "$c2"
checkout_at "$tmp/rerun-forward" "$c1"
expect "re-run with a newer release moves the checkout to it, not to main" \
  "$(run_install "$tmp/rerun-forward")" "$c2"

checkout_at "$tmp/rerun-db-newer" "$c1"
schema 3
expect "re-run with a release older than the database is refused" \
  "$(run_install "$tmp/rerun-db-newer")" refused
expect "  ... and the checkout is left where it was" \
  "$(git -C "$tmp/rerun-db-newer" rev-parse HEAD)" "$c1"
schema ""

checkout_at "$tmp/rerun-older" "$c3"
expect "re-run with a release older than the checkout is refused" \
  "$(run_install "$tmp/rerun-older")" refused
expect "  ... and the checkout is left where it was" \
  "$(git -C "$tmp/rerun-older" rev-parse HEAD)" "$c3"
if ! grep -q "jabali update" "$tmp/rerun-older.log" 2>/dev/null; then
  echo "FAIL   ... the refusal does not point at 'jabali update'"
  fail=1
fi

manifest "$c3"
checkout_at "$tmp/rerun-same" "$c3"
expect "re-run with the release the server already runs" \
  "$(run_install "$tmp/rerun-same")" "$c3"

# --- a stable hotfix, which is not on main --------------------------------------
# Checkouts cloned before the hotfix exists, as on a server installed earlier.
checkout_at "$tmp/rerun-hotfix-from-main" "$c3"
checkout_at "$tmp/rerun-hotfix-from-older" "$c1"
git -C "$remote" checkout -q -b hotfix "$c1"
echo fix >"$remote/fix.txt"
git -C "$remote" add -A
git -C "$remote" commit -q -m hotfix
h1=$(git -C "$remote" rev-parse HEAD)
git -C "$remote" tag release-hotfix "$h1"
git -C "$remote" checkout -q main
git -C "$remote" branch -q -D hotfix

manifest "$h1"
expect "fresh install of a hotfix release (reachable only by a tag)" \
  "$(run_install "$tmp/fresh-hotfix")" "$h1"
expect "re-run fetches a hotfix release that isn't on main" \
  "$(run_install "$tmp/rerun-hotfix-from-older")" "$h1"
expect "a hotfix release with fewer migrations than the checkout is refused" \
  "$(run_install "$tmp/rerun-hotfix-from-main")" refused
expect "  ... and the checkout is left where it was" \
  "$(git -C "$tmp/rerun-hotfix-from-main" rev-parse HEAD)" "$c3"

# A server on the hotfix moves on to the next release cut from main.
manifest "$c3"
checkout_at "$tmp/rerun-from-hotfix" "$h1"
expect "re-run from a hotfix to a newer main release" \
  "$(run_install "$tmp/rerun-from-hotfix")" "$c3"

# A git server that won't hand out a commit by its id: the release commit then
# comes with the tags, including a `stable` tag that moved since this checkout
# last fetched it.
git -C "$remote" tag stable "$c1"
checkout_at "$tmp/rerun-moved-tag" "$c1"
git -C "$remote" checkout -q -b hotfix2 "$c1"
echo fix2 >"$remote/fix2.txt"
git -C "$remote" add -A
git -C "$remote" commit -q -m hotfix2
h2=$(git -C "$remote" rev-parse HEAD)
git -C "$remote" tag -f stable "$h2" >/dev/null
git -C "$remote" checkout -q main
git -C "$remote" branch -q -D hotfix2
manifest "$h2"
got=$(
  eval "real_$(fn root_git)"
  root_git() {
    if [[ "$1" == "fetch" && "$2" == "origin" && "$3" =~ ^[0-9a-f]{40}$ ]]; then
      return 1
    fi
    real_root_git "$@"
  }
  run_install "$tmp/rerun-moved-tag"
)
expect "without fetch by id, the tags bring the release, though a local tag moved" \
  "$got" "$h2"

exit $fail

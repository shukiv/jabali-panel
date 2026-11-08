#!/usr/bin/env bash
# install/tests/test_install_source_follows_stable.sh — a source install
# (README's classic `curl …/main/install.sh | bash`) installs the stable
# release, as get.jabali-panel.com does.
#
# It built the newest main, while the server then followed the stable channel,
# so a new server started ahead of stable. main() now first hands over to the
# stable release's own install.sh (follow_stable_release), which checks the
# stable commit out (JABALI_INSTALL_COMMIT) with the guard a prebuilt release
# has: never onto an older release than the server runs.
#
# Behavioural: follow_stable_release runs with curl, mariadb and exec stubbed;
# clone_or_update_repo runs against a local git remote, as in
# test_install_pins_release_commit.sh.
#
# Run from repo root:
#     bash install/tests/test_install_source_follows_stable.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Captured into a variable, never piped: see test_nginx_server_names_hash_on_update.sh.
fn() { awk -v name="$1" '$0 ~ "^"name"\\(\\) \\{$" {p=1} p {print} p && /^\}$/ {exit}' install.sh; }

for f in follow_stable_release _stable_tag_commit _installed_release_channel _source_install_commit curl_ipv4_retry \
         root_git clone_or_update_repo _prebuilt_commit _fetch_release_commit \
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

expect() { # expect <case> <got> <want>
  if [[ "$2" == "$3" ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1"
    echo "       got:  $2"
    echo "       want: $3"
    fail=1
  fi
}

# --- main() hands over before anything runs ----------------------------------
main_src=$(fn main)
first=$(awk 'NR > 1 && !/^[[:space:]]*(#|$)/ {print; exit}' <<<"$main_src")
expect "main() starts with follow_stable_release" "$first" "  follow_stable_release"
first_line() { awk -v s="$1" 'index($0, s) == 1 {print NR; exit}' install.sh; }
args_line=$(first_line '_cli_args=("$@")')
loop_line=$(first_line 'while [[ $# -gt 0 ]]; do')
if [[ -n "$args_line" && -n "$loop_line" && "$args_line" -lt "$loop_line" ]]; then
  echo "ok   the arguments are kept before the flag parsing consumes them"
else
  echo "FAIL the arguments aren't kept before the flag parsing (line ${args_line:-none} vs ${loop_line:-none})"
  fail=1
fi

# --- follow_stable_release ---------------------------------------------------
_log()  { :; }
_ok()   { :; }
_warn() { echo "warn: $*" >&2; }
_die()  { echo "die: $*" >&2; exit 1; }
# Replaces the process in install.sh; here it records what would have run.
exec() { printf '%s\n' "$*" >"$tmp/exec"; exit 0; }

S=1111111111111111111111111111111111111111   # the stable commit
T=2222222222222222222222222222222222222222   # an annotated tag object
mkdir -p "$tmp/bin"
# curl -fsSL --retry 3 -o <file> <url>: info/refs from $FAKE_REFS, the stable
# install.sh from $FAKE_STABLE_SH. Every URL asked for goes to $CURL_LOG.
cat >"$tmp/bin/curl" <<'EOF'
#!/bin/bash
out="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) [ "$1" = --retry ] && shift; shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "$url" >>"$CURL_LOG"
case "$url" in
  https://github.com/shukiv/jabali-panel.git/info/refs\?service=git-upload-pack)
    [ -z "$FAKE_REFS_DOWN" ] || exit 22
    cp "$FAKE_REFS" "$out" ;;
  https://raw.githubusercontent.com/shukiv/jabali-panel/*/install.sh)
    cp "$FAKE_STABLE_SH" "$out" ;;
  *) exit 22 ;;
esac
EOF
# The server's release channel comes from $FAKE_CHANNEL: empty is a new server.
cat >"$tmp/bin/mariadb" <<'EOF'
#!/bin/sh
case "$*" in
  *release_channel*) [ -n "$FAKE_CHANNEL" ] && echo "$FAKE_CHANNEL" ;;
  *information_schema*) if [ -s "$FAKE_SCHEMA_FILE" ]; then echo 1; else echo 0; fi ;;
  *schema_migrations*) cat "$FAKE_SCHEMA_FILE" 2>/dev/null ;;
esac
exit 0
EOF
printf '#!/bin/sh\nexit 0\n' >"$tmp/bin/sudo"
chmod +x "$tmp/bin/curl" "$tmp/bin/mariadb" "$tmp/bin/sudo"
PATH="$tmp/bin:$PATH"
export CURL_LOG="$tmp/curl.log" FAKE_REFS="$tmp/refs" FAKE_STABLE_SH="$tmp/stable-install.sh"
export FAKE_SCHEMA_FILE="$tmp/schema" FAKE_CHANNEL="" FAKE_REFS_DOWN=""
REPO_BRANCH=main

# A ref advertisement as GitHub serves it: pkt-lines, a 4-hex length before each.
refs() {
  {
    printf '001e# service=git-upload-pack\n0000'
    printf '0155%s HEAD\0multi_ack side-band-64k\n' "3333333333333333333333333333333333333333"
    printf '003f%s refs/heads/main\n' "3333333333333333333333333333333333333333"
    local line
    for line in "$@"; do printf '%04x%s\n' $((${#line} + 5)) "$line"; done
    printf '0000'
  } >"$FAKE_REFS"
}
stable_sh_knows_install_commit() {
  printf '#!/usr/bin/env bash\n_source_install_commit() {\n  :\n}\n' >"$FAKE_STABLE_SH"
}
stable_sh_predates_it() {
  printf '#!/usr/bin/env bash\nclone_or_update_repo() {\n  :\n}\n' >"$FAKE_STABLE_SH"
}

# follow <env...>: follow_stable_release with these variables; prints what ran
# instead of the install ("exec <command>", the temporary directory shown as
# DIR), "continued" when this install.sh goes on, or "died".
follow() {
  : >"$CURL_LOG"
  rm -f "$tmp/exec"
  if (
    unset JABALI_INSTALL_COMMIT JABALI_PREBUILT_BIN JABALI_RELEASE_MANIFEST \
      JABALI_REPO_URL JABALI_REPO_BRANCH JABALI_RELEASE_CHANNEL
    [[ $# -eq 0 ]] || export "$@"
    _cli_args=(--hostname h.example.test)
    follow_stable_release
    echo continued >"$tmp/exec"
  ) 2>"$tmp/follow.err"; then
    sed -E 's#bash /[^ ]*/install\.sh#bash DIR/install.sh#' "$tmp/exec"
  else
    echo died
  fi
}
asked() { if [[ -s "$CURL_LOG" ]]; then echo "asked GitHub"; else echo "asked nothing"; fi; }

handover="env JABALI_INSTALL_COMMIT=$S JABALI_RELEASE_CHANNEL=stable bash DIR/install.sh --hostname h.example.test"

refs "$S refs/tags/stable"
stable_sh_knows_install_commit
expect "a source install continues as the stable release's install.sh, with the same arguments" \
  "$(follow)" "$handover"
expect "    ... downloaded from the stable commit" \
  "$(grep -c -F "https://raw.githubusercontent.com/shukiv/jabali-panel/$S/install.sh" "$CURL_LOG")" 1

refs "$T refs/tags/stable" "$S refs/tags/stable^{}"
expect "an annotated stable tag gives the commit it names, not the tag object" \
  "$(follow)" "$handover"

refs "$S refs/tags/stable"
stable_sh_predates_it
expect "a stable install.sh that predates this leaves the install on main" "$(follow)" continued
grep -q "installing the newest main" "$tmp/follow.err" \
  && echo "ok       ... and says so" || { echo "FAIL     ... without saying so"; fail=1; }
stable_sh_knows_install_commit

expect "JABALI_RELEASE_CHANNEL=development installs main" \
  "$(follow JABALI_RELEASE_CHANNEL=development) $(asked)" "continued asked nothing"
FAKE_CHANNEL=development
expect "a server on the development channel stays on main" "$(follow) $(asked)" "continued asked nothing"
FAKE_CHANNEL=stable
expect "a server on the stable channel follows stable" "$(follow)" "$handover"
FAKE_CHANNEL=""
expect "JABALI_REPO_BRANCH is still the branch installed" \
  "$(follow JABALI_REPO_BRANCH=feature) $(asked)" "continued asked nothing"
expect "JABALI_REPO_URL (a mirror) is still installed from" \
  "$(follow JABALI_REPO_URL=https://git.example.test/jabali.git) $(asked)" "continued asked nothing"
expect "the stable release's install.sh doesn't hand over again" \
  "$(follow JABALI_INSTALL_COMMIT=$S) $(asked)" "continued asked nothing"
expect "an install from get.jabali-panel.com is left to the release it chose" \
  "$(follow JABALI_RELEASE_MANIFEST=$tmp/MANIFEST JABALI_PREBUILT_BIN=$tmp/prebuilt) $(asked)" \
  "continued asked nothing"

refs
expect "no stable tag stops the install" "$(follow)" died
grep -q "JABALI_RELEASE_CHANNEL=development" "$tmp/follow.err" \
  && echo "ok       ... and names JABALI_RELEASE_CHANNEL=development" \
  || { echo "FAIL     ... without naming JABALI_RELEASE_CHANNEL=development"; fail=1; }
refs "$S refs/tags/stable"
expect "GitHub unreachable stops the install" "$(follow FAKE_REFS_DOWN=1)" died
expect "an unknown channel stops the install" "$(follow JABALI_RELEASE_CHANNEL=nightly)" died
# A box without curl: PATH holds only what the check needs, and no curl.
mkdir -p "$tmp/nocurl"
ln -s "$(command -v mktemp)" "$tmp/nocurl/mktemp"
ln -s "$tmp/bin/mariadb" "$tmp/nocurl/mariadb"
expect "no curl stops the install" "$(follow PATH="$tmp/nocurl")" died
grep -q "curl is needed" "$tmp/follow.err" \
  && echo "ok       ... and says curl is needed" \
  || { echo "FAIL     ... without saying curl is needed"; fail=1; }
unset -f exec

# --- clone_or_update_repo checks out JABALI_INSTALL_COMMIT ------------------
root_repo_layout() { :; }
getent() { return 0; }
# The test runs unprivileged: report an existing checkout as root-owned.
stat() {
  if [[ "$1" == "-c" && "$2" == "%u" ]]; then
    [[ -e "$3" ]] || return 1
    echo 0
  else
    command stat "$@"
  fi
}
_prebuilt_ready() { return 1; }

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
REPO_TOKEN=""
LOG_FILE=/dev/null
: >"$FAKE_SCHEMA_FILE"

# install <dir> [commit]: clone_or_update_repo for <dir> with JABALI_INSTALL_COMMIT
# set to commit; prints the checkout's HEAD, or "refused".
install() {
  local out
  if out=$( (REPO_DIR="$1"; JABALI_INSTALL_COMMIT="${2:-}"; clone_or_update_repo) 2>&1 ); then
    git -C "$1" rev-parse HEAD
  else
    printf '%s\n' "$out" >"$1.log"
    echo refused
  fi
}
checkout_at() { # checkout_at <dir> <commit>: an installed server's checkout
  git clone -q --branch main "$REPO_URL" "$1"
  git -C "$1" reset -q --hard "$2"
}

expect "a new server gets the stable commit, not the newest main" "$(install "$tmp/new" "$c2")" "$c2"
checkout_at "$tmp/older" "$c1"
expect "a server on an older release moves up to stable" "$(install "$tmp/older" "$c2")" "$c2"
checkout_at "$tmp/ahead" "$c3"
expect "a server ahead of stable is refused" "$(install "$tmp/ahead" "$c2")" refused
expect "    ... and its checkout is left where it was" "$(git -C "$tmp/ahead" rev-parse HEAD)" "$c3"
expect "a JABALI_INSTALL_COMMIT that isn't a commit id is refused" "$(install "$tmp/bad" "stable")" refused
expect "without it, a source install still gets the newest main" "$(install "$tmp/main")" "$c3"

exit $fail

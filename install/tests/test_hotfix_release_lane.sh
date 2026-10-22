#!/usr/bin/env bash
# A hotfix release reaches stable servers, and nothing else.
#
# A hotfix branch (hotfix/<name>) is the stable commit plus fixes cherry-picked
# from main. release-hotfix.yml, dispatched from main, builds its release;
# `jabali release promote` then moves `stable` to it. This checks the parts
# that keep it contained:
#   - tools/check-hotfix-branch.sh passes only a branch built on stable whose
#     commits are cherry-picks of main commits (or say "Hotfix-only:");
#   - the release is a pre-release that never becomes Latest and has no
#     fixed-name asset, so fresh installs and servers on main never get it;
#   - bootstrap.sh's API fallback skips pre-releases;
#   - release.yml publishes only main.
#
# Run from repo root:
#     bash install/tests/test_hotfix_release_lane.sh
set -uo pipefail

fail=0
repo_root=$(pwd)
check="$repo_root/tools/check-hotfix-branch.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
g() { git -C "$tmp/r" "$@"; }
commit() { # <file> <message...>
  echo "$RANDOM" >>"$tmp/r/$1"
  g add "$1" && g commit -q -m "${*:2}"
}

# main: s0, s (promoted to stable), then fix f1 and feature x. A side branch
# has a commit that never reached main.
git init -q -b main "$tmp/r"
commit a "s0: an older build"
s0=$(g rev-parse HEAD)
commit a "s: the stable build"
g tag stable
commit b "f1: the security fix"
f1=$(g rev-parse HEAD)
commit c "x: a feature main has, stable must not get"
g switch -q -c side stable
commit d "side: never on main"
side=$(g rev-parse HEAD)
g switch -q main

expect() { # <want: pass|fail> <description> <branch> [<text the output must have>]
  local out rc
  out=$(cd "$tmp/r" && bash "$check" refs/tags/stable "$3" main 2>&1)
  rc=$?
  case "$1:$rc" in
    pass:0 | fail:1) ;;
    *) echo "FAIL: $2: want $1, exit $rc: $out"; fail=1; return ;;
  esac
  if [[ -n "${4:-}" ]] && ! grep -qF -- "$4" <<<"$out"; then
    echo "FAIL: $2: output lacks '$4': $out"
    fail=1
  fi
}

g switch -q -c hotfix/ok stable
g cherry-pick -x "$f1" >/dev/null
g switch -q main
expect pass "a cherry-pick -x of a main commit" hotfix/ok

expect pass "the stable commit itself" refs/tags/stable

g switch -q -c hotfix/plain stable
commit e "a fix written on the hotfix branch only"
g switch -q main
expect fail "a commit that is not a cherry-pick" hotfix/plain "is not a cherry-pick of a commit on main"

g switch -q -c hotfix/only stable
echo "$RANDOM" >>"$tmp/r/e"
g add e && g commit -q -m "stable-only workaround" -m "Hotfix-only: main replaced this code"
g switch -q main
expect pass "a commit with a Hotfix-only: line" hotfix/only

g switch -q -c hotfix/side stable
g cherry-pick -x "$side" >/dev/null
g switch -q main
expect fail "a cherry-pick of a commit that is not on main" hotfix/side "which is not on main"

expect fail "main itself: built on stable, but with main's own commits" main "is not a cherry-pick of a commit on main"

# Every commit on it is a good cherry-pick, but it starts before stable.
g switch -q -c hotfix/old "$s0"
g cherry-pick -x "$f1" >/dev/null
g switch -q main
expect fail "a branch built on an older commit than stable" hotfix/old "is not built on"

# Every commit on it would pass on its own; the merge itself is what fails.
g switch -q -c picks stable
g cherry-pick -x "$f1" >/dev/null
g switch -q -c hotfix/merge stable
echo "$RANDOM" >>"$tmp/r/f"
g add f && g commit -q -m "stable-only tweak" -m "Hotfix-only: test"
g merge -q --no-ff -m "merge picks" -m "Hotfix-only: test" picks >/dev/null
g switch -q main
expect fail "a branch with a merge commit" hotfix/merge "has merge commits"

(cd "$tmp/r" && bash "$check" refs/tags/stable >/dev/null 2>&1)
[[ $? == 2 ]] || { echo "FAIL: wrong usage does not exit 2"; fail=1; }

# bootstrap.sh API fallback: newest release first, pre-releases skipped.
fn_src=$(awk '/^release_tarball_urls\(\) \{$/,/^\}$/' bootstrap.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: release_tarball_urls not defined in bootstrap.sh"
  fail=1
else
  eval "$fn_src"
  d=https://github.com/shukiv/jabali-panel/releases/download
  got=$(release_tarball_urls <<EOF
[
  {
    "tag_name": "release-aaaaaaaaa",
    "draft": false,
    "prerelease": true,
    "assets": [
      { "name": "jabali-release-aaaaaaaaa.tar.gz", "browser_download_url": "$d/release-aaaaaaaaa/jabali-release-aaaaaaaaa.tar.gz" },
      { "name": "jabali-release-aaaaaaaaa.tar.gz.sha256", "browser_download_url": "$d/release-aaaaaaaaa/jabali-release-aaaaaaaaa.tar.gz.sha256" }
    ]
  },
  {
    "tag_name": "release-bbbbbbbbb",
    "draft": false,
    "prerelease": false,
    "assets": [
      { "name": "jabali-release-bbbbbbbbb.tar.gz", "browser_download_url": "$d/release-bbbbbbbbb/jabali-release-bbbbbbbbb.tar.gz" },
      { "name": "jabali-release.tar.gz", "browser_download_url": "$d/release-bbbbbbbbb/jabali-release.tar.gz" }
    ]
  },
  {
    "tag_name": "release-ccccccccc",
    "draft": false,
    "prerelease": false,
    "assets": [
      { "name": "jabali-release-ccccccccc.tar.gz", "browser_download_url": "$d/release-ccccccccc/jabali-release-ccccccccc.tar.gz" }
    ]
  }
]
EOF
)
  want="$d/release-bbbbbbbbb/jabali-release-bbbbbbbbb.tar.gz
$d/release-ccccccccc/jabali-release-ccccccccc.tar.gz"
  [[ "$got" == "$want" ]] || { echo "FAIL: bootstrap fallback picked:"; echo "$got"; fail=1; }
fi

# The hotfix release is a pre-release, never Latest, with no fixed-name asset.
wf=.github/workflows/release-hotfix.yml
grep -qF '"prerelease": True' "$wf" || { echo "FAIL: $wf does not publish a pre-release"; fail=1; }
grep -qF '"make_latest": "false"' "$wf" || { echo "FAIL: $wf can make the hotfix release Latest"; fail=1; }
if grep -qE 'upload_asset +"?dist/jabali-release\.tar\.gz' "$wf"; then
  echo "FAIL: $wf uploads the fixed-name asset fresh installs download"
  fail=1
fi
grep -qF 'tools/check-hotfix-branch.sh refs/tags/stable "$head" HEAD' "$wf" || { echo "FAIL: $wf does not check the hotfix"; fail=1; }
# Dispatched from main, so main's check and publish steps run (a push would
# run the hotfix tree's copies, which are stable's). It builds and gates the
# commit it checked, not whatever the branch points at later.
grep -qE '^  workflow_dispatch:$' "$wf" || { echo "FAIL: $wf is not run by dispatch"; fail=1; }
if grep -qE '^  push:' "$wf"; then echo "FAIL: $wf runs on push"; fail=1; fi
grep -qF "    if: github.ref == 'refs/heads/main'" "$wf" || { echo "FAIL: $wf provenance does not require main"; fail=1; }
n=$(grep -cF 'ref: ${{ needs.provenance.outputs.sha }}' "$wf")
[[ "$n" == 2 ]] || { echo "FAIL: $wf does not build and gate the checked commit ($n)"; fail=1; }
grep -qF 'ref: ${{ inputs.ref }}' .github/workflows/migrations-gate.yml \
  || { echo "FAIL: migrations-gate.yml cannot test a given commit"; fail=1; }
# release.yml publishes main only, both jobs.
n=$(grep -cE "^    if: github.ref == 'refs/heads/main'$" .github/workflows/release.yml)
[[ "$n" == 2 ]] || { echo "FAIL: release.yml does not limit both jobs to main ($n)"; fail=1; }

if ((fail == 0)); then
  echo "PASS: hotfix releases reach stable servers only, from stable plus fixes on main"
  exit 0
fi
echo "RESULT: FAIL"
exit 1

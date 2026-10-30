#!/usr/bin/env bash
# Every move of the stable tag gets an entry in CHANGELOG.md, written by
# tools/changelog-entry.sh. This checks that the entry:
#   - names the date, the kind of move and the commit stable now points at;
#   - lists every change merged since the old stable, oldest first, one line
#     per first-parent commit, the PR number as a link;
#   - names the main commit a hotfix commit was copied from;
#   - counts the migrations added and names the ones that change rows;
#   - is refused for a stable that did not move forward.
#
# Run from repo root:
#     bash install/tests/test_changelog_entry.sh
set -uo pipefail

fail=0
repo_root=$(pwd)
entry="$repo_root/tools/changelog-entry.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
g() { git -C "$tmp/r" "$@"; }
mig=panel-api/internal/db/migrations
commit() { # <message> [<file> <content>]...
  local msg=$1
  shift
  while (($# >= 2)); do
    mkdir -p "$tmp/r/$(dirname "$1")"
    printf '%s\n' "$2" >"$tmp/r/$1"
    g add "$1"
    shift 2
  done
  k=$((k + 1)) # a file of its own, so a cherry-pick applies cleanly
  echo "$k" >"$tmp/r/c$k" && g add "c$k"
  g commit -q -m "$msg"
}
k=0

git init -q -b main "$tmp/r"
commit "s: the old stable" "$mig/000004_old.up.sql" "CREATE TABLE old (id INT);"
old=$(g rev-parse HEAD)
commit "Mail: a mailbox setting (GH #10) (#11)" \
  "$mig/000005_mailbox_setting.up.sql" "ALTER TABLE mailboxes ADD COLUMN x INT;" \
  "$mig/000005_mailbox_setting.down.sql" "ALTER TABLE mailboxes DROP COLUMN x;"
commit "Backups: fill the new column (#12)" \
  "$mig/000006_fill_x.up.sql" "-- fill it
  update mailboxes SET x = 1;"
fix=$(g rev-parse HEAD)
commit "Docs: a page with no PR number"
new=$(g rev-parse HEAD)
g switch -q -c hotfix/x "$old"
g cherry-pick -x "$fix" >/dev/null
hot=$(g rev-parse HEAD)
g switch -q main

run() { (cd "$tmp/r" && bash "$entry" "$@" 2>&1); }
has() { # <description> <output> <text the output must have>
  if ! grep -qF -- "$3" <<<"$2"; then
    echo "FAIL: $1: output lacks '$3':"
    echo "$2"
    fail=1
  fi
}

out=$(run "$old" "$new" --date 2026-11-01)
rc=$?
[[ $rc -eq 0 ]] || { echo "FAIL: monthly entry: exit $rc: $out"; fail=1; }
has "heading" "$out" "## 2026-11-01 — monthly cut: \`${new:0:9}\`"
has "range" "$out" "\`stable\` moved from \`${old:0:9}\` to \`${new:0:9}\`: 3 changes."
has "migrations" "$out" "Migrations 000005–000006 (2). Changing rows: 000006."
has "PR link" "$out" "- \`$(g rev-parse --short=9 "$new~2")\` Mail: a mailbox setting (GH #10) ([#11](https://github.com/shukiv/jabali-panel/pull/11))"
has "no PR" "$out" "- \`${new:0:9}\` Docs: a page with no PR number"
order=$(grep -oE '^- `[0-9a-f]+` [A-Za-z]+' <<<"$out" | awk '{print $3}' | paste -sd' ')
[[ "$order" == "Mail Backups Docs" ]] || { echo "FAIL: order oldest first: got '$order'"; fail=1; }
grep -qF "s: the old stable" <<<"$out" && { echo "FAIL: the old stable itself is listed"; fail=1; }

out=$(run "$old" "$hot" --hotfix --date 2026-10-21)
rc=$?
[[ $rc -eq 0 ]] || { echo "FAIL: hotfix entry: exit $rc: $out"; fail=1; }
has "hotfix heading" "$out" "## 2026-10-21 — hotfix: \`${hot:0:9}\`"
has "hotfix links main's PR" "$out" "- \`${hot:0:9}\` Backups: fill the new column ([#12](https://github.com/shukiv/jabali-panel/pull/12), copied from \`${fix:0:9}\`)"
has "hotfix migrations" "$out" "Migrations 000006 (1). Changing rows: 000006."

out=$(run "$new~1" "$new" --date 2026-11-02)
has "no migrations" "$out" "No migrations."

out=$(run "$new" "$hot")
rc=$?
[[ $rc -eq 1 ]] || { echo "FAIL: a stable that jumps to another line: want exit 1, got $rc: $out"; fail=1; }
out=$(run "$new" "$new")
rc=$?
[[ $rc -eq 1 ]] || { echo "FAIL: a stable that did not move: want exit 1, got $rc: $out"; fail=1; }
out=$(run nope "$new")
rc=$?
[[ $rc -eq 1 ]] || { echo "FAIL: not a commit: want exit 1, got $rc: $out"; fail=1; }
out=$(run "$old")
rc=$?
[[ $rc -eq 2 ]] || { echo "FAIL: missing argument: want exit 2, got $rc: $out"; fail=1; }
out=$(run "$old" "$new" --date 1.11.2026)
rc=$?
[[ $rc -eq 2 ]] || { echo "FAIL: bad date: want exit 2, got $rc: $out"; fail=1; }

if ((fail)); then
  echo "test_changelog_entry: FAIL"
  exit 1
fi
echo "test_changelog_entry: OK"

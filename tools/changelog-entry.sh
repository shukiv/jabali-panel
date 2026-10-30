#!/usr/bin/env bash
# changelog-entry.sh <old-stable> <new-stable> [--hotfix] [--date YYYY-MM-DD]
# — print the CHANGELOG.md entry for one move of the `stable` tag.
#
# Run it right after `stable` moves (the monthly cut on the 1st, or a hotfix)
# and paste the output above the newest entry in CHANGELOG.md. The entry lists
# every change since <old-stable>, oldest first: one line per first-parent
# commit, which on main is one squash-merged PR. A hotfix commit copied with
# `git cherry-pick -x` keeps the PR number and names the commit it came from.
# The entry also counts the migrations added and names the ones whose up file
# changes rows (an UPDATE, DELETE, INSERT or REPLACE statement).
#
# --date defaults to today (UTC).
#
# Exit status: 0 with the entry on stdout, 1 when <new-stable> is not a move
# forward from <old-stable>, 2 on bad usage.
set -euo pipefail

repo_url=https://github.com/shukiv/jabali-panel
mig_dir=panel-api/internal/db/migrations

usage() {
  echo "usage: $0 <old-stable> <new-stable> [--hotfix] [--date YYYY-MM-DD]" >&2
  exit 2
}

kind="monthly cut"
day=$(date -u +%F)
refs=()
while (($#)); do
  case "$1" in
    --hotfix) kind=hotfix ;;
    --date)
      (($# >= 2)) || usage
      day=$2
      shift
      ;;
    -*) usage ;;
    *) refs+=("$1") ;;
  esac
  shift
done
((${#refs[@]} == 2)) || usage
[[ "$day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || usage

for ref in "${refs[@]}"; do
  if ! git rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
    echo "changelog-entry: not a commit: $ref" >&2
    exit 1
  fi
done
old=$(git rev-parse "${refs[0]}^{commit}")
new=$(git rev-parse "${refs[1]}^{commit}")
if [[ "$old" == "$new" ]] || ! git merge-base --is-ancestor "$old" "$new"; then
  echo "changelog-entry: $new is not a move forward from $old" >&2
  exit 1
fi

short() { git rev-parse --short=9 "$1"; }

# pr_of <subject> — the PR number of a squash merge: the trailing "(#N)".
pr_of() {
  if [[ "$1" =~ \(#([0-9]+)\)$ ]]; then
    echo "${BASH_REMATCH[1]}"
  fi
}

# line <commit> — one list item: short hash, subject, PR link, and the main
# commit a cherry-pick came from.
line() {
  local subject pr src extra=()
  subject=$(git log -1 --format=%s "$1")
  pr=$(pr_of "$subject")
  [[ -n "$pr" ]] && subject=${subject% (#"$pr")}
  src=$(git log -1 --format=%b "$1" | sed -n 's/^(cherry picked from commit \([0-9a-f]\{7,\}\))$/\1/p' | tail -n 1)
  if [[ -n "$src" ]] && git rev-parse --verify --quiet "$src^{commit}" >/dev/null; then
    src=$(short "$src")
  fi
  [[ -n "$pr" ]] && extra+=("[#$pr]($repo_url/pull/$pr)")
  [[ -n "$src" ]] && extra+=("copied from \`${src:0:9}\`")
  if ((${#extra[@]})); then
    local joined
    joined=$(printf '%s, ' "${extra[@]}")
    subject+=" (${joined%, })"
  fi
  printf -- '- `%s` %s\n' "$(short "$1")" "$subject"
}

mapfile -t commits < <(git log --first-parent --reverse --format=%H "$old..$new")
n=${#commits[@]}
noun=changes
((n == 1)) && noun=change

mapfile -t ups < <(git diff --name-only --diff-filter=A "$old" "$new" -- "$mig_dir/" | grep '\.up\.sql$' | sort)
migrations="No migrations."
if ((${#ups[@]})); then
  nums=() rows=()
  for f in "${ups[@]}"; do
    num=$(basename "$f")
    num=${num%%_*}
    nums+=("$num")
    if git show "$new:$f" | grep -qiE '^[[:space:]]*(update|delete|insert|replace)[[:space:]]'; then
      rows+=("$num")
    fi
  done
  range=${nums[0]}
  ((${#nums[@]} > 1)) && range="${nums[0]}–${nums[-1]}"
  migrations="Migrations $range (${#nums[@]})."
  if ((${#rows[@]})); then
    list=$(printf '%s, ' "${rows[@]}")
    migrations+=" Changing rows: ${list%, }."
  fi
fi

echo "## $day — $kind: \`$(short "$new")\`"
echo
echo "\`stable\` moved from \`$(short "$old")\` to \`$(short "$new")\`: $n $noun."
echo
echo "$migrations"
echo
for c in "${commits[@]}"; do
  line "$c"
done

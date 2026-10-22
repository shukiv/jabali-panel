#!/usr/bin/env bash
# check-hotfix-branch.sh <stable> <head> <main> — refuse a hotfix build that
# would give stable-channel servers anything other than the stable build plus
# fixes main already has.
#
# A hotfix branch starts at the commit the `stable` tag points at and carries
# fixes that landed on main first, copied with `git cherry-pick -x`.
# release-hotfix.yml runs this before it builds anything. It fails when:
#   - <head> is not built on <stable>: stable-channel servers would jump to a
#     different line of history, not to stable plus the fix;
#   - the branch has a merge commit: a merge can bring in anything;
#   - a commit is not a cherry-pick of a commit on <main> and has no
#     "Hotfix-only: <reason>" line saying why main does not need it.
# <head> equal to <stable> passes: that builds the release for the current
# stable commit itself.
#
# Exit status: 0 when the branch passes, 1 when it does not, 2 on bad usage.
set -euo pipefail

if (($# != 3)); then
  echo "usage: $0 <stable> <head> <main>" >&2
  exit 2
fi
stable=$1 head=$2 main=$3
for ref in "$stable" "$head" "$main"; do
  if ! git rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
    echo "check-hotfix-branch: not a commit: $ref" >&2
    exit 1
  fi
done

if ! git merge-base --is-ancestor "$stable" "$head"; then
  echo "check-hotfix-branch: $head is not built on $stable ($(git rev-parse --short "$stable^{commit}"))." >&2
  echo "Start the branch at the stable commit: git switch -c hotfix/<name> $(git rev-parse --short "$stable^{commit}")" >&2
  exit 1
fi

merges=$(git rev-list --merges "$stable..$head")
if [[ -n "$merges" ]]; then
  echo "check-hotfix-branch: the branch has merge commits; cherry-pick the fixes instead:" >&2
  # shellcheck disable=SC2086 # one commit id per word
  git log --no-walk --format='  %h %s' $merges >&2
  exit 1
fi

fail=0
while read -r c; do
  [[ -n "$c" ]] || continue
  msg=$(git log -1 --format=%B "$c")
  subject="$(git log -1 --format='%h %s' "$c")"
  if grep -qE '^Hotfix-only: *[^ ]' <<<"$msg"; then
    echo "  $subject: hotfix only ($(sed -nE 's/^Hotfix-only: *//p' <<<"$msg" | head -1))"
    continue
  fi
  picked=$(sed -nE 's/^\(cherry picked from commit ([0-9a-f]{40})\)$/\1/p' <<<"$msg" | tail -1)
  if [[ -z "$picked" ]]; then
    echo "check-hotfix-branch: $subject is not a cherry-pick of a commit on main." >&2
    echo "  Land the fix on main first, then: git cherry-pick -x <commit on main>." >&2
    echo "  If main does not need this change, add a line 'Hotfix-only: <reason>' to its message." >&2
    fail=1
    continue
  fi
  if ! git merge-base --is-ancestor "$picked" "$main" 2>/dev/null; then
    echo "check-hotfix-branch: $subject was cherry-picked from $picked, which is not on main." >&2
    fail=1
    continue
  fi
  echo "  $subject: cherry-picked from main ${picked:0:9}"
done < <(git rev-list --reverse "$stable..$head")

if ((fail == 0)); then
  echo "check-hotfix-branch: $(git rev-parse --short "$head^{commit}") is stable ($(git rev-parse --short "$stable^{commit}")) plus fixes from main"
fi
exit "$fail"

#!/usr/bin/env bash
# Apply Jabali's patches (patches/*.patch, in name order) to an unpacked
# Snuffleupagus source tree. GH #2262.
#
# Usage: apply-patches.sh <source-root>   apply; print the stamp suffix
#        apply-patches.sh --id            print the patch-set id only
#
# All patches or none: they are tried on a copy first, and if one does not
# apply (a new upstream version moved the code), the tree stays as upstream
# shipped it. A plain upstream build keeps PHP Defense running; a failed
# build would leave PHP with no extension at all.
#
# Suffix printed: "" (no patches), "+patch-<id>" (applied) or
# "+patch-failed-<id>". build.sh puts it in the version stamp, so a changed
# patch set rebuilds once, and a set that does not apply is not retried on
# every update until it changes.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

shopt -s nullglob
patches=("$HERE"/patches/*.patch)
shopt -u nullglob

patch_id() {
  if (( ${#patches[@]} > 0 )); then
    cat "${patches[@]}" | sha256sum | cut -c1-16
  fi
}

ARG="${1:?usage: apply-patches.sh <source-root> | --id}"
if [[ "$ARG" == "--id" ]]; then
  patch_id
  exit 0
fi

SRC="$ARG"
(( ${#patches[@]} > 0 )) || exit 0
id="$(patch_id)"

apply_all() {
  local dir="$1" p
  for p in "${patches[@]}"; do
    patch -d "$dir" -p1 -N -t -s --no-backup-if-mismatch <"$p" >/dev/null 2>&1 || {
      echo "apply-patches.sh: $(basename "$p") does not apply; building the upstream source" >&2
      return 1
    }
  done
}

if ! command -v patch >/dev/null 2>&1; then
  echo "apply-patches.sh: patch(1) is not installed; building the upstream source" >&2
  echo "+patch-failed-$id"
  exit 0
fi

trial="$(mktemp -d)"
trap 'rm -rf "$trial"' EXIT
cp -a "$SRC/." "$trial/"
if ! apply_all "$trial"; then
  echo "+patch-failed-$id"
  exit 0
fi
apply_all "$SRC"
echo "+patch-$id"

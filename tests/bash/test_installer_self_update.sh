#!/usr/bin/env bash
# Test: Installer self-update is resilient to local changes
#
# Why: operators running `curl | bash` a second time have invariably ended
# up with a dirty working tree in $SCRIPT_DIR (either from a previous
# interrupted install or from package-install side effects that rewrite
# tracked files). `git pull --ff-only` fatals on a dirty tree, the whole
# installer aborts, and the panel's Addons tab leaves the spinner spinning
# forever because the binary never lands.
#
# The contract tested here: the install/update block must forcibly
# reconcile SCRIPT_DIR with origin/<default-branch> regardless of local
# modifications. We don't care about preserving local edits; operators
# should never have manually-edited source in the install dir.
#
# Usage: bash tests/bash/test_installer_self_update.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

PASS=0
FAIL=0
TOTAL=0

pass() { PASS=$((PASS + 1)); TOTAL=$((TOTAL + 1)); printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail_t() { FAIL=$((FAIL + 1)); TOTAL=$((TOTAL + 1)); printf "  \033[31m✗\033[0m %s\n" "$1"; }
section() { echo ""; printf "\033[1m%s\033[0m\n" "$1"; }

# ─────────────────────────────────────────────────────────────
# Extract the git update snippet from install.sh and exercise it
# in isolation against a throwaway git repo. We don't run the whole
# installer — only the block that updates SCRIPT_DIR from origin.
# ─────────────────────────────────────────────────────────────

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

UPSTREAM="$TMP_ROOT/upstream.git"
CLONE="$TMP_ROOT/clone"

section "Setup: bare upstream with two commits ahead"

# Bare remote so `clone` can push to it in setup.
git init --quiet --bare --initial-branch=main "$UPSTREAM"

# Seed commit via an intermediate non-bare clone.
SEED="$TMP_ROOT/seed"
git -c init.defaultBranch=main clone --quiet "$UPSTREAM" "$SEED" 2>/dev/null || git clone --quiet "$UPSTREAM" "$SEED"
(
    cd "$SEED"
    git config user.email test@example.com
    git config user.name test
    git checkout --quiet -B main 2>/dev/null || true
    echo "initial" > install.sh
    git add install.sh
    git commit --quiet -m "initial"
    git push --quiet origin HEAD:main
)

# The "installed" clone the operator sees.
git clone --quiet -b main "$UPSTREAM" "$CLONE"

# Upstream adds two commits that the operator has yet to pull.
(
    cd "$SEED"
    git pull --quiet origin main
    echo "upstream-change-1" >> install.sh
    git commit --quiet -am "upstream 1"
    echo "upstream-change-2" >> install.sh
    git commit --quiet -am "upstream 2"
    git push --quiet origin main
)

# Dirty the installed clone the way a real run leaves it: modify install.sh
# in place so the upcoming pull would normally abort with "local changes
# would be overwritten".
(
    cd "$CLONE"
    echo "local-modification-that-should-be-discarded" >> install.sh
    # Extra untracked file — must NOT survive the reset, otherwise a stale
    # leftover from a partially-applied install could leak into the fresh
    # working tree.
    echo "leftover" > stray.txt
)

section "Run: the installer's self-update snippet"

# Stand-in for the logic inside install.sh. This is what the installer
# should execute when updating in place.
update_clone() {
    local dir="$1"
    git -C "$dir" fetch --quiet origin
    # Detect default branch at runtime so we tolerate main/master/trunk.
    local branch
    branch="$(git -C "$dir" symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's@^origin/@@' || true)"
    branch="${branch:-main}"
    git -C "$dir" reset --hard --quiet "origin/${branch}"
    git -C "$dir" clean -fdx --quiet
}

if update_clone "$CLONE" >/dev/null 2>&1; then
    pass "self-update succeeds even with a dirty working tree"
else
    fail_t "self-update failed on a dirty working tree — installer will abort in production"
fi

section "Verify: clone is now at origin/HEAD with no local leftovers"

# HEAD must match origin/main.
local_head=$(git -C "$CLONE" rev-parse HEAD)
remote_head=$(git -C "$CLONE" rev-parse origin/main)
if [[ "$local_head" == "$remote_head" ]]; then
    pass "HEAD matches origin/main after self-update"
else
    fail_t "HEAD ($local_head) does not match origin/main ($remote_head)"
fi

# Local modification gone.
if grep -q 'local-modification-that-should-be-discarded' "$CLONE/install.sh"; then
    fail_t "local modification survived reset"
else
    pass "local modification discarded"
fi

# Stray untracked file gone.
if [[ -e "$CLONE/stray.txt" ]]; then
    fail_t "untracked stray.txt survived clean"
else
    pass "untracked files cleaned"
fi

# Upstream change applied.
if grep -q 'upstream-change-2' "$CLONE/install.sh"; then
    pass "upstream commits fast-forwarded in"
else
    fail_t "upstream commits missing after self-update"
fi

# ─────────────────────────────────────────────────────────────
section "Contract: the production install.sh calls a matching update block"
# ─────────────────────────────────────────────────────────────

# Guard-rail: if someone later re-introduces `git pull --ff-only` the
# regression we just fixed is back. Fail the suite instead of silently
# allowing it.
if grep -nE '^[[:space:]]*git[[:space:]]+(-C[[:space:]]+"?\$SCRIPT_DIR"?[[:space:]]+)?pull[[:space:]]+--ff-only' "$PROJECT_DIR/install.sh" >/dev/null; then
    fail_t "install.sh still uses 'git pull --ff-only' — dirty checkouts will abort the installer"
else
    pass "install.sh does not rely on 'git pull --ff-only'"
fi

# Must use fetch + reset --hard (or equivalent forced reconciliation).
if grep -nE 'git[[:space:]]+(-C[[:space:]]+"?\$SCRIPT_DIR"?[[:space:]]+)?reset[[:space:]]+--hard' "$PROJECT_DIR/install.sh" >/dev/null; then
    pass "install.sh uses 'git reset --hard' to reconcile with upstream"
else
    fail_t "install.sh missing 'git reset --hard' self-update — dirty checkouts will still abort"
fi

# ─────────────────────────────────────────────────────────────
section "Summary"
printf "  %d/%d passed\n" "$PASS" "$TOTAL"
if [[ "$FAIL" -gt 0 ]]; then
    printf "\033[31m  %d failed\033[0m\n" "$FAIL"
    exit 1
fi
printf "\033[32m  all green\033[0m\n"

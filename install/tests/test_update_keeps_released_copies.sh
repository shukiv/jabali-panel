#!/usr/bin/env bash
# install/tests/test_update_keeps_released_copies.sh — GH #2440. Keep every
# repo file a released `jabali update` still copies.
#
# `jabali update` runs the steps of the binary the server already has, against
# the newly fetched checkout. When #2395 deleted install/ssh/jabali-nspawn-enter,
# the "sync static assets" step of every earlier release (and of main's own
# update.go) still copied it under `set -e`, so every update stopped there and
# no server got a new build.
#
# Checked here:
#   1. each file released update steps copy is in the tree;
#   2. the retired helper kept for them is a stub that does nothing;
#   3. the current update step removes the helper and its sudoers file
#      instead of installing them.
#
# Run from repo root:
#     bash install/tests/test_update_keeps_released_copies.sh
set -euo pipefail

cd "$(dirname "$0")/../.."
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

ok()  { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }

# 1. Repo paths the update steps of the 2026-11-01 stable (db6d0f4da) and the
# builds after it read from the checkout. Build outputs are left out.
# install/ssh/jabali-nspawn-sudoers is left out on purpose: those steps check
# it with `visudo -cf ... &&`, which a missing file only skips, and a sudo
# grant must not come back.
released_copies=(
  install.sh
  install/docker-apps
  install/nginx/jabali-fastcgi-cache.conf
  install/php/jabali-php-pool.conf.tmpl
  install/phpmyadmin/sso.php
  install/ssh/jabali-nspawn-enter
  install/ssh/jabali-sftp.conf
  install/ssh/normalize-ssh-classic.sh
  panel-ui
  wp-plugins/jabali-cache
)
for p in "${released_copies[@]}"; do
  if [[ -e "$p" ]]; then ok "released updates find $p"; else bad "$p is gone, so released updates stop when they copy it"; fi
done

# 2. The stub: the released step installs it (as root, 0755); running it does
# nothing and fails.
stub=install/ssh/jabali-nspawn-enter
if [[ -f "$stub" ]]; then
  if (set -e; install -m 0755 "$stub" "$tmp/jabali-nspawn-enter"); then
    ok "the released step's install of the stub succeeds"
  else
    bad "installing the stub fails"
  fi
  if out=$("$tmp/jabali-nspawn-enter" debian-12-v1 2>&1); then
    bad "the stub exits 0"
  elif [[ "$out" == *retired* ]]; then
    ok "the stub refuses and says it is retired"
  else
    bad "the stub fails without saying it is retired: $out"
  fi
  if grep -v '^#' "$stub" | grep -q -E 'systemd-nspawn|machinectl|(^|[^a-z-])(exec|sudo)([^a-z-]|$)'; then
    bad "the stub runs something; it must only print and exit"
  else
    ok "the stub runs nothing"
  fi
fi

# 3. The current "sync static assets" step.
step=$(awk '/\{"sync static assets", func\(\) error \{/ {p=1} p {print} p && /^\t\t\}\},$/ {exit}' panel-api/cmd/server/update.go)
if [[ -z "$step" ]]; then
  bad "the sync static assets step was not found in update.go"
else
  if grep -q 'install/ssh/jabali-nspawn-enter\|install/ssh/jabali-nspawn-sudoers' <<<"$step"; then
    bad "update still installs the retired nspawn helper or its sudoers file"
  else
    ok "update no longer installs the nspawn helper or its sudoers file"
  fi
  if grep -q 'rm -f /etc/sudoers.d/jabali-nspawn /usr/local/bin/jabali-nspawn-enter' <<<"$step"; then
    ok "update removes the helper and its sudoers file"
  else
    bad "update does not remove the helper and its sudoers file"
  fi
fi

exit $fail

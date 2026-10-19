#!/usr/bin/env bash
# install/tests/test_panel_cert_hook_no_mail_server.sh — the certbot deploy
# hook leaves Stalwart alone on a box without the mail server (GH #2166).
#
# `jabali update` installs jabali-stalwart.service on every box, with or
# without the mail module, but only a mail box has the jabali-mail user the
# unit runs as and the Stalwart binary. Starting the unit anywhere else fails
# at step USER and restarts in a loop, each failure a "Stalwart is down"
# alert. So the kind=mail arm starts, pushes into and restarts Stalwart only
# when stalwart_installed says both are there; it still installs the cert
# files and reloads nginx.
#
# Behavioural for stalwart_installed (the hook sourced in library mode),
# source-level for the arm (running it writes under /etc).
#
# Run from repo root:
#     bash install/tests/test_panel_cert_hook_no_mail_server.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

hook="install/letsencrypt/jabali-panel-cert.sh"
fail=0
finished=""

tmp=$(mktemp -d)
# Sourcing the hook without library mode runs its top-level code, whose
# `exit 0` would end this test with success. Any exit before the end of the
# script is a failure.
trap 'rm -rf "$tmp"; if [[ -z "$finished" ]]; then echo "FAIL: test ended early — does the hook support JABALI_PANEL_CERT_HOOK_LIB=1?"; exit 1; fi' EXIT

JABALI_PANEL_CERT_HOOK_LIB=1
# shellcheck source=/dev/null
source "$hook"
set +e
if ! declare -F stalwart_installed >/dev/null; then
  echo "FAIL: $hook does not define stalwart_installed"
  fail=1
else
  printf '#!/bin/sh\n' >"$tmp/stalwart"
  chmod 0755 "$tmp/stalwart"
  me="$(id -un)"

  STALWART_USER="$me" STALWART_BIN="$tmp/stalwart"
  stalwart_installed || { echo "FAIL: user and binary present, but stalwart_installed says no"; fail=1; }

  STALWART_USER="jabali-no-such-user-2166" STALWART_BIN="$tmp/stalwart"
  stalwart_installed && { echo "FAIL: no Stalwart user, but stalwart_installed says yes"; fail=1; }

  STALWART_USER="$me" STALWART_BIN="$tmp/absent"
  stalwart_installed && { echo "FAIL: no Stalwart binary, but stalwart_installed says yes"; fail=1; }
fi
set -e

# The kind=mail arm's non-comment lines, up to its closing `    ;;`.
arm=$(awk '
  $0 == "  mail)" { inside = 1; next }
  inside && $0 == "    ;;" { exit }
  inside && $0 !~ /^[[:space:]]*#/ { print }
' "$hook")
if [[ -z "$arm" ]]; then
  echo "FAIL: case arm 'mail)' not found in $hook"
  fail=1
fi

# Every Stalwart step sits inside `if stalwart_installed; then … fi`, and the
# nginx reload stays outside it.
guarded=$(awk '
  /^[[:space:]]*if stalwart_installed; then/ { depth = 1; next }
  depth && /^[[:space:]]*if / { depth++ }
  depth && /^[[:space:]]*fi([[:space:]]|;|$)/ { depth--; next }
  depth { print }
' <<<"$arm")
for step in 'systemctl start jabali-stalwart' 'jabali-stalwart-push-cert' 'systemctl restart jabali-stalwart'; do
  if grep -qF "$step" <<<"$arm" && ! grep -qF "$step" <<<"$guarded"; then
    echo "FAIL: kind=mail runs '$step' outside 'if stalwart_installed' — a box without the mail server starts a Stalwart that can't run"
    fail=1
  fi
done
if grep -qF 'systemctl reload nginx' <<<"$guarded"; then
  echo "FAIL: kind=mail reloads nginx only when Stalwart is installed — webmail's cert must reload either way"
  fail=1
fi
if ! grep -qF 'systemctl reload nginx' <<<"$arm"; then
  echo "FAIL: kind=mail no longer reloads nginx"
  fail=1
fi

finished=1
if [[ "$fail" -eq 0 ]]; then
  echo "OK: panel cert hook leaves Stalwart alone on a box without the mail server"
else
  exit 1
fi

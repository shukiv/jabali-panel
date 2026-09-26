#!/usr/bin/env bash
# install/tests/test_panel_cert_hook_stalwart_push_order.sh — the certbot
# deploy hook must push a renewed mail certificate into Stalwart BEFORE it
# restarts Stalwart.
#
# Stalwart serves TLS from its x:Certificate registry and loads that registry
# only at startup; writing a new entry does not hot-reload it. A hook that
# restarts first and pushes second leaves IMAPS/SMTPS (:993/:465) on the
# PREVIOUS certificate and key until something else restarts Stalwart —
# observed on the test box during the JAB-357 key rotation (:993 kept the old
# cert after a successful renewal; a manual restart fixed it).
#
# Asserts, source-level (no host mutation), for both Stalwart branches:
#   kind=mail         the push precedes the restart, and Stalwart is started
#                     (no-op when running) before the push so the push can
#                     reach its management API.
#   kind=mail-domain  the push precedes the restart (already correct; guarded
#                     here so the two branches cannot drift apart again).
#
# Run from repo root:
#     bash install/tests/test_panel_cert_hook_stalwart_push_order.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

hook="install/letsencrypt/jabali-panel-cert.sh"
fail=0

if [[ ! -f "$hook" ]]; then
  echo "FAIL: $hook not found"
  exit 1
fi

# branch_lines KIND — "<lineno>:<text>" for the non-comment lines of the
# `  KIND)` case arm, up to its closing `    ;;`.
branch_lines() {
  awk -v arm="  $1)" '
    $0 == arm { inside = 1; next }
    inside && $0 == "    ;;" { exit }
    inside && $0 !~ /^[[:space:]]*#/ { print NR ":" $0 }
  ' "$hook"
}

# first_line KIND REGEX / last_line KIND REGEX — line number of the first/last
# matching directive in the arm, empty when absent.
first_line() { branch_lines "$1" | grep -E "$2" | head -1 | cut -d: -f1 || true; }
last_line()  { branch_lines "$1" | grep -E "$2" | tail -1 | cut -d: -f1 || true; }

push_re='/usr/local/bin/jabali-stalwart-push-cert( |$)'
restart_re='systemctl restart jabali-stalwart'
start_re='systemctl start jabali-stalwart'

for kind in mail mail-domain; do
  if [[ -z "$(branch_lines "$kind")" ]]; then
    echo "FAIL: case arm '$kind)' not found in $hook"
    fail=1
    continue
  fi
  push_first=$(first_line "$kind" "$push_re")
  push_last=$(last_line "$kind" "$push_re")
  restart_first=$(first_line "$kind" "$restart_re")
  if [[ -z "$push_first" ]]; then
    echo "FAIL: kind=$kind never runs jabali-stalwart-push-cert — Stalwart keeps the old certificate"
    fail=1
    continue
  fi
  if [[ -z "$restart_first" ]]; then
    echo "FAIL: kind=$kind never restarts jabali-stalwart — the pushed certificate is never loaded"
    fail=1
    continue
  fi
  if (( restart_first < push_last )); then
    echo "FAIL: kind=$kind restarts jabali-stalwart (line $restart_first) before the push (line $push_last) — IMAPS/SMTPS keep serving the previous certificate"
    fail=1
  fi
done

# kind=mail: Stalwart must be up for the push to reach its management API.
start_ln=$(first_line mail "$start_re")
push_ln=$(first_line mail "$push_re")
if [[ -z "$start_ln" ]]; then
  echo "FAIL: kind=mail does not ensure jabali-stalwart is running before the push — a stopped Stalwart comes back on the old certificate"
  fail=1
elif [[ -n "$push_ln" ]] && (( start_ln > push_ln )); then
  echo "FAIL: kind=mail starts jabali-stalwart (line $start_ln) after the push (line $push_ln)"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "OK: panel cert hook pushes mail certificates into Stalwart before restarting it"
else
  exit 1
fi

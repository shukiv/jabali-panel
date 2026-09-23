#!/usr/bin/env bash
# install/tests/test_stalwart_queryrecipient_group_parity.sh — GH #1818.
#
# Stalwart's SQL directory queryRecipient decides which addresses are valid
# recipients. It lives in TWO places that must stay identical:
#   - install/stalwart/apply-plan.json.tmpl — the fresh-install base, and
#   - install.sh — the ADR-0073 converger, which OVERWRITES the live x:Directory
#     query fields on every install/update (so on any existing box the converger
#     wins; the apply-plan base never does).
#
# M51 (712ca9587) added mail_groups member-expansion to the apply-plan copy but
# NOT to the converger, so on every installed host a message to group@domain
# resolved to zero rows and Stalwart returned 550 5.1.2 — mail groups never
# worked. This guard fails if the two copies drift again, and specifically if
# either loses the group expansion or the send_only exclusion.
#
# Run from repo root:
#     bash install/tests/test_stalwart_queryrecipient_group_parity.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

install_q=$(grep -oP '(?<=local query_recipient=")[^"]*' install.sh | head -1)
plan_q=$(grep -oP '(?<="queryRecipient": ")[^"]*' install/stalwart/apply-plan.json.tmpl | head -1)

if [[ -z "$install_q" ]]; then
  echo "FAIL: could not extract query_recipient from install.sh"
  exit 1
fi
if [[ -z "$plan_q" ]]; then
  echo "FAIL: could not extract queryRecipient from apply-plan.json.tmpl"
  exit 1
fi

if [[ "$install_q" != "$plan_q" ]]; then
  echo "FAIL: install.sh queryRecipient has drifted from apply-plan.json.tmpl (GH #1818)."
  echo "  The converger overwrites the live config, so a drift ships silently."
  echo "  install.sh : $install_q"
  echo "  apply-plan : $plan_q"
  fail=1
fi

# Both must fan a mail group out to its members, or group@domain 550s (GH #1818).
for label in "install.sh:$install_q" "apply-plan:$plan_q"; do
  name=${label%%:*}
  q=${label#*:}
  if [[ "$q" != *"mail_group_members"* ]]; then
    echo "FAIL: $name queryRecipient has no mail_group_members expansion — groups will 550 (GH #1818)"
    fail=1
  fi
  # send-only mailboxes must not be delivery recipients (GH #371), incl. as group members.
  if [[ "$q" != *"send_only = 0"* ]]; then
    echo "FAIL: $name queryRecipient dropped the send_only = 0 exclusion (GH #371)"
    fail=1
  fi
done

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: queryRecipient is identical across install.sh and apply-plan.json.tmpl, with group expansion + send_only exclusion"
fi
exit "$fail"

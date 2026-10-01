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
# The expansion must also SKIP plain distribution lists: those are native
# Stalwart mailing lists, and a directory match on the list address shadows the
# list, so delivery fails with "Mailbox not found" (GH #1818, verified on
# Stalwart 0.16.15). Only groups projected as a Group account (resource groups,
# internal-only distribution lists) need the member rows to receive at all.
#
# GH #1816 / ADR-0170: both queries must also join domains and require
# ownership_status = 'verified' in every branch (the postmaster fallback
# included), so an address on an unproven name is never a local recipient. queryEmailAliases gets the same parity
# check, and so does queryLogin, so a mailbox on an unproven name cannot sign
# in.
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
  # A plain distribution list is a Stalwart mailing list; expanding it here would
  # shadow the list and bounce every message (GH #1818).
  if [[ "$q" != *"(g.group_kind <> 'distribution' OR g.internal_only = 1)"* ]]; then
    echo "FAIL: $name queryRecipient expands plain distribution lists — they must resolve as Stalwart mailing lists (GH #1818)"
    fail=1
  fi
  # send-only mailboxes must not be delivery recipients (GH #371), incl. as group members.
  if [[ "$q" != *"send_only = 0"* ]]; then
    echo "FAIL: $name queryRecipient dropped the send_only = 0 exclusion (GH #371)"
    fail=1
  fi
done

install_a=$(grep -oP '(?<=local query_aliases=")[^"]*' install.sh | head -1)
plan_a=$(grep -oP '(?<="queryEmailAliases": ")[^"]*' install/stalwart/apply-plan.json.tmpl | head -1)
if [[ -z "$install_a" || -z "$plan_a" ]]; then
  echo "FAIL: could not extract queryEmailAliases from install.sh and apply-plan.json.tmpl"
  exit 1
fi
if [[ "$install_a" != "$plan_a" ]]; then
  echo "FAIL: install.sh queryEmailAliases has drifted from apply-plan.json.tmpl."
  echo "  install.sh : $install_a"
  echo "  apply-plan : $plan_a"
  fail=1
fi

# GH #1816: an unproven name is never a local recipient. The direct mailbox
# branch, the group address, the group member, the alias lookup and the
# postmaster fallback (ADR-0110) each require a verified domain (5 in
# queryRecipient), and so do both branches of the alias list.
verified="ownership_status = 'verified'"
for label in "install.sh:$install_q" "apply-plan:$plan_q"; do
  name=${label%%:*}
  q=${label#*:}
  n=$(grep -o "$verified" <<<"$q" | wc -l)
  if [[ "$n" -ne 5 ]]; then
    echo "FAIL: $name queryRecipient has $n verified-domain filters, want 5 (GH #1816)"
    fail=1
  fi
  if [[ "$q" != *"pd.$verified"* ]]; then
    echo "FAIL: $name queryRecipient resolves postmaster@ of an unproven domain to the admin (GH #1816)"
    fail=1
  fi
done
for label in "install.sh:$install_a" "apply-plan:$plan_a"; do
  name=${label%%:*}
  q=${label#*:}
  if [[ "$q" != *" d.$verified"* ]]; then
    echo "FAIL: $name queryEmailAliases lists aliases on unproven domains (GH #1816)"
    fail=1
  fi
  if [[ "$q" != *"pd.$verified"* ]]; then
    echo "FAIL: $name queryEmailAliases lists postmaster@ of unproven domains on the admin's mailbox (GH #1816)"
    fail=1
  fi
done

# GH #1816: a mailbox on an unproven domain cannot sign in (and so cannot send
# as that name). queryLogin keeps send-only accounts (they submit mail) and
# disabled ones out as before.
install_l=$(grep -oP '(?<=local query_login=")[^"]*' install.sh | head -1)
plan_l=$(grep -oP '(?<="queryLogin": ")[^"]*' install/stalwart/apply-plan.json.tmpl | head -1)
if [[ -z "$install_l" || -z "$plan_l" ]]; then
  echo "FAIL: could not extract queryLogin from install.sh and apply-plan.json.tmpl"
  exit 1
fi
if [[ "$install_l" != "$plan_l" ]]; then
  echo "FAIL: install.sh queryLogin has drifted from apply-plan.json.tmpl."
  echo "  install.sh : $install_l"
  echo "  apply-plan : $plan_l"
  fail=1
fi
for label in "install.sh:$install_l" "apply-plan:$plan_l"; do
  name=${label%%:*}
  q=${label#*:}
  if [[ "$q" != *" d.$verified"* ]]; then
    echo "FAIL: $name queryLogin lets a mailbox on an unproven domain sign in (GH #1816)"
    fail=1
  fi
  if [[ "$q" != *"is_disabled = 0"* ]]; then
    echo "FAIL: $name queryLogin dropped the is_disabled = 0 gate"
    fail=1
  fi
  if [[ "$q" == *"send_only"* ]]; then
    echo "FAIL: $name queryLogin filters send_only — send-only accounts must still sign in to submit (GH #371)"
    fail=1
  fi
done

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: queryLogin, queryRecipient and queryEmailAliases are identical across install.sh and apply-plan.json.tmpl, with group expansion (plain distribution lists excluded) + send_only exclusion + verified-domain filter"
fi
exit "$fail"

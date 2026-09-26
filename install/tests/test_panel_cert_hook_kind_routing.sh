#!/usr/bin/env bash
# install/tests/test_panel_cert_hook_kind_routing.sh — how the certbot deploy
# hook decides which panel cert a renewed lineage is.
#
# certbot's unattended renewals carry no JABALI_PANEL_CERT_KIND; the lineage
# name is the only signal. The panel MAIL cert's lineage is not always
# mail.<current-hostname>:
#   - JAB-389 pins the mail cert to the name it was seeded with, so after a
#     panel hostname change it stays mail.<old-hostname>;
#   - JAB-390 lets the shared mail hostname be any FQDN (mx.example.net).
# Before this test such a lineage fell through to "not ours" and its
# renewals were silently never deployed — panel-mail.crt expired on day 90.
# The hook now records the mail lineage it deployed and routes renewals by
# that record, keeping every other rule (tenant lineages are never touched).
#
# Behavioural: sources the hook in library mode and calls panel_cert_kind.
#
# Run from repo root:
#     bash install/tests/test_panel_cert_hook_kind_routing.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

hook="install/letsencrypt/jabali-panel-cert.sh"
fail=0
finished=""

tmp=$(mktemp -d)
# Sourcing a hook without library mode runs its top-level code, whose
# `exit 0` would end this test with success. Any exit before the end of the
# script is a failure.
trap 'rm -rf "$tmp"; if [[ -z "$finished" ]]; then echo "FAIL: test ended early — does the hook support JABALI_PANEL_CERT_HOOK_LIB=1 and define panel_cert_kind?"; exit 1; fi' EXIT

JABALI_PANEL_CERT_HOOK_LIB=1
# shellcheck source=/dev/null
source "$hook"
set +e
if ! declare -F panel_cert_kind >/dev/null; then
  echo "FAIL: $hook does not define panel_cert_kind"
  exit 1
fi
PANEL_MAIL_LINEAGE_FILE="$tmp/panel-mail.lineage"

cn=panel.example.com
check() { # check DESC WANT LINEAGE
  local got
  got=$(panel_cert_kind "/etc/letsencrypt/live/$3" "$cn")
  if [[ "$got" != "$2" ]]; then
    echo "FAIL: $1: lineage $3 -> '${got}', want '$2'"
    fail=1
  fi
}

# --- no record yet (existing boxes before their next mail deploy) ---
unset JABALI_MAIL_DOMAIN_ID
check "hostname lineage"                hostname "panel.example.com"
check "derived mail lineage"            mail     "mail.panel.example.com"
check "tenant mail lineage is ignored"  ""       "mail.tenant.com"
check "tenant lineage is ignored"       ""       "tenant.com"
check "custom mail name, no record"     ""       "mx.example.net"

# --- per-domain mail issuance sets JABALI_MAIL_DOMAIN_ID ---
JABALI_MAIL_DOMAIN_ID=01TENANTDOMAINID0000000000 check "per-domain mail lineage" mail-domain "mail.tenant.com"

# --- recorded mail lineage: a custom shared mail hostname (JAB-390) ---
printf 'mx.example.net\n' >"$PANEL_MAIL_LINEAGE_FILE"
check "recorded custom mail lineage"    mail     "mx.example.net"
check "hostname still hostname"         hostname "panel.example.com"
check "tenant still ignored"            ""       "mail.tenant.com"

# --- recorded mail lineage after a panel hostname change (JAB-389) ---
printf 'mail.old-panel.example.com\n' >"$PANEL_MAIL_LINEAGE_FILE"
check "pinned mail lineage after rename" mail    "mail.old-panel.example.com"
check "old hostname lineage not panel"   ""      "old-panel.example.com"

# --- an empty record never matches ---
: >"$PANEL_MAIL_LINEAGE_FILE"
check "empty record"                     ""      "mx.example.net"

# --- the mail arm records the lineage it deployed (source-level: the arm
# itself needs root to run) ---
mail_arm=$(awk '$0 == "  mail)" {inside = 1; next} inside && $0 == "    ;;" {exit} inside && $0 !~ /^[[:space:]]*#/' "$hook")
if ! grep -q 'PANEL_MAIL_LINEAGE_FILE' <<<"$mail_arm"; then
  echo "FAIL: the kind=mail arm does not record its lineage in \$PANEL_MAIL_LINEAGE_FILE — renewals of a non-derived mail lineage are never deployed"
  fail=1
fi

finished=1
if [[ "$fail" -eq 0 ]]; then
  echo "OK: panel cert hook routes hostname, derived/recorded mail, per-domain mail and tenant lineages correctly"
else
  exit 1
fi

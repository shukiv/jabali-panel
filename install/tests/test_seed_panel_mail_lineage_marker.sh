#!/usr/bin/env bash
# install/tests/test_seed_panel_mail_lineage_marker.sh — boxes whose panel
# mail cert was deployed before the deploy hook started recording its
# lineage get the record seeded on `jabali update`.
#
# Without the record, a mail lineage that is not mail.<current-hostname>
# (the mail cert stays pinned to mail.<old-hostname> after a panel rename,
# JAB-389) is treated as a tenant lineage and its renewals are never
# deployed. seed_panel_mail_lineage_marker writes the record from the
# deployed cert's CN when a matching Let's Encrypt lineage exists, and never
# overwrites an existing record.
#
# Behavioural: extracts the function from install.sh and runs it against
# temp directories.
#
# Run from repo root:
#     bash install/tests/test_seed_panel_mail_lineage_marker.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn_src=$(awk '/^seed_panel_mail_lineage_marker\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: seed_panel_mail_lineage_marker() not found in install.sh"
  exit 1
fi
eval "$fn_src"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# mkbox NAME CN WITH_LINEAGE — a fake /etc/jabali/tls + /etc/letsencrypt/live
mkbox() {
  local box="$tmp/$1"
  mkdir -p "$box/tls" "$box/live"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
    -subj "/CN=$2" -keyout /dev/null -out "$box/tls/panel-mail.crt" 2>/dev/null
  if [[ "$3" == yes ]]; then
    mkdir -p "$box/live/$2"
  fi
  echo "$box"
}

# 1. renamed panel: mail cert pinned to mail.old-panel.example.com, LE lineage exists -> seeded
b=$(mkbox renamed mail.old-panel.example.com yes)
seed_panel_mail_lineage_marker "$b/tls" "$b/live"
if [[ "$(cat "$b/tls/panel-mail.lineage" 2>/dev/null)" != "mail.old-panel.example.com" ]]; then
  echo "FAIL: renamed box: record not seeded from the deployed cert's CN"
  fail=1
fi

# 2. self-signed mail cert (no LE lineage) -> nothing written
b=$(mkbox selfsigned mail.panel.example.com no)
seed_panel_mail_lineage_marker "$b/tls" "$b/live"
if [[ -e "$b/tls/panel-mail.lineage" ]]; then
  echo "FAIL: self-signed box: a record was written for a cert with no Let's Encrypt lineage"
  fail=1
fi

# 3. existing record is never overwritten
b=$(mkbox recorded mail.old-panel.example.com yes)
printf 'mx.example.net\n' >"$b/tls/panel-mail.lineage"
seed_panel_mail_lineage_marker "$b/tls" "$b/live"
if [[ "$(cat "$b/tls/panel-mail.lineage")" != "mx.example.net" ]]; then
  echo "FAIL: an existing record was overwritten"
  fail=1
fi

# 4. no deployed mail cert -> nothing, no error
mkdir -p "$tmp/nocert/tls" "$tmp/nocert/live"
if ! seed_panel_mail_lineage_marker "$tmp/nocert/tls" "$tmp/nocert/live"; then
  echo "FAIL: no mail cert must be a quiet no-op"
  fail=1
fi
if [[ -e "$tmp/nocert/tls/panel-mail.lineage" ]]; then
  echo "FAIL: record written without a deployed mail cert"
  fail=1
fi

# 5. wired into the update path
body=$(awk '/^provision_new_software\(\) *\{/ {inside = 1; next} inside && /^\}/ {exit} inside' install.sh)
if ! grep -q 'seed_panel_mail_lineage_marker' <<<"$body"; then
  echo "FAIL: provision_new_software does not call seed_panel_mail_lineage_marker — existing boxes never get the record"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "OK: panel mail lineage record is seeded on update from the deployed cert, never overwritten"
else
  exit 1
fi

#!/usr/bin/env bash
# install/tests/test_panel_hsts_selfsigned.sh — regression coverage for GH #1507
# (fresh install: panel :8443 serves a self-signed cert WITH an HSTS header, which
# hard-blocks the browser with no exception path).
#
# HSTS over a self-signed cert is a lockout: the browser pins strict-transport but
# can't validate the cert, so there's no "add exception" path to the panel. The
# fix serves the HSTS header via an include snippet whose content is toggled by
# cert kind — empty while self-signed, the header once a CA cert is deployed.
#
# Asserts, source-level (no host mutation):
#   1. The panel vhost template includes the snippet, and hardcodes NO raw HSTS.
#   2. install.sh's writer gates HSTS-ON on a NON-"Jabali Panel" issuer (i.e.
#      self-signed → OFF), fail-safe.
#   3. The snippet is written BEFORE `nginx -t` (a missing include fails the test,
#      taking :8443 down on upgrade).
#   4. jabali-panel-cert.sh writes HSTS ON in the hostname (LE) branch.
#   5. Drift: the ON `add_header` line is byte-identical in install.sh and cert.sh.
#
# Run from repo root:  bash install/tests/test_panel_hsts_selfsigned.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

tmpl="install/nginx/jabali-panel-vhost.conf.tmpl"
installer="install.sh"
hook="install/letsencrypt/jabali-panel-cert.sh"
snippet_inc="/etc/nginx/snippets/jabali-panel-hsts.conf"
hsts_line='add_header Strict-Transport-Security "max-age=31536000" always;'
fail=0

for f in "$tmpl" "$installer" "$hook"; do
  [[ -f "$f" ]] || { echo "FAIL: $f not found"; exit 1; }
done

# --- 1. template includes the snippet, no raw HSTS left ---
if grep -q 'Strict-Transport-Security' "$tmpl"; then
  echo "FAIL: $tmpl still hardcodes a Strict-Transport-Security header — HSTS must come from the toggled snippet"
  fail=1
fi
inc_count=$(grep -c "include ${snippet_inc};" "$tmpl" || true)
if [[ "$inc_count" -lt 1 ]]; then
  echo "FAIL: $tmpl does not include ${snippet_inc}"
  fail=1
fi

# --- 2. install.sh writer: HSTS ON only for a non-self-signed issuer ---
if ! grep -q '!= "Jabali Panel"' "$installer"; then
  echo "FAIL: install.sh HSTS writer must gate ON on issuer != 'Jabali Panel' (self-signed stays OFF)"
  fail=1
fi

# --- 3. the snippet is written BEFORE nginx -t inside install_nginx_panel_vhost ---
write_ln=$(grep -nE '_write_panel_hsts_snippet "\$tls_cert"' "$installer" | head -1 | cut -d: -f1)
test_ln=$(grep -nE 'testing nginx configuration' "$installer" | head -1 | cut -d: -f1)
if [[ -z "$write_ln" ]]; then
  echo "FAIL: install_nginx_panel_vhost never calls _write_panel_hsts_snippet — the include would be missing"
  fail=1
elif [[ -n "$test_ln" && "$write_ln" -ge "$test_ln" ]]; then
  echo "FAIL: the HSTS snippet write (line $write_ln) must precede 'nginx -t' (line $test_ln) or the vhost fails validation"
  fail=1
fi

# --- 4. cert.sh hostname (LE) branch enables HSTS ---
if ! grep -qF "$hsts_line" "$hook"; then
  echo "FAIL: jabali-panel-cert.sh must write the HSTS header ON when a CA cert is deployed"
  fail=1
fi

# --- 5. drift: the ON line is byte-identical in both writers ---
if ! grep -qF "$hsts_line" "$installer"; then
  echo "FAIL: install.sh HSTS-ON body drifted from the canonical header line"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "OK: panel HSTS is snippet-toggled — off on self-signed, on for a CA cert (GH #1507)"
else
  exit 1
fi

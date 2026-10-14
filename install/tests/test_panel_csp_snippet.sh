#!/usr/bin/env bash
# install/tests/test_panel_csp_snippet.sh — the panel vhost's CSP comes from
# /etc/nginx/snippets/jabali-panel-csp.conf (Server Settings -> Support chat;
# the agent's nginx.panel_csp.apply owns the file).
#
# Asserts, source-level (no host mutation):
#   1. The template includes the snippet once.
#   2. install_nginx_panel_vhost seeds the snippet BEFORE `nginx -t` (a missing
#      include fails the test and takes :8443 down on upgrade).
#   3. The seed is guarded by "only if missing": `jabali update` re-runs
#      install_nginx_panel_vhost, and overwriting would turn an admin's
#      enabled support chat off (its CSP) until the next panel start.
# The byte-equality of the seed and the agent's chat-off rendering is pinned
# in Go (panel-agent TestPanelCSP_InstallSeedMatchesTheAgent).
#
# Run from repo root:  bash install/tests/test_panel_csp_snippet.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

tmpl="install/nginx/jabali-panel-vhost.conf.tmpl"
installer="install.sh"
snippet="/etc/nginx/snippets/jabali-panel-csp.conf"
fail=0

inc_count=$(grep -c "^  include ${snippet};" "$tmpl" || true)
if [[ "$inc_count" -ne 1 ]]; then
  echo "FAIL: $tmpl must include ${snippet} exactly once at server level (found $inc_count)"
  fail=1
fi

# The function body, so the line checks below can't match elsewhere.
body=$(awk '/^install_nginx_panel_vhost\(\) \{/{on=1} on{print} on && /^\}/{exit}' "$installer")
if [[ -z "$body" ]]; then
  echo "FAIL: install_nginx_panel_vhost not found in $installer"
  exit 1
fi

guard_ln=$(grep -nF "if [[ ! -f ${snippet} ]]; then" <<<"$body" | head -1 | cut -d: -f1 || true)
seed_ln=$(grep -nF "cat > ${snippet} <<'PANEL_CSP_OFF'" <<<"$body" | head -1 | cut -d: -f1 || true)
test_ln=$(grep -nF 'testing nginx configuration' <<<"$body" | head -1 | cut -d: -f1 || true)

if [[ -z "$seed_ln" ]]; then
  echo "FAIL: install_nginx_panel_vhost never seeds ${snippet}"
  fail=1
elif [[ -z "$test_ln" || "$seed_ln" -ge "$test_ln" ]]; then
  echo "FAIL: the CSP seed (line $seed_ln) must precede 'nginx -t' (line ${test_ln:-?})"
  fail=1
fi
if [[ -z "$guard_ln" || -z "$seed_ln" || $((seed_ln - guard_ln)) -ne 1 ]]; then
  echo "FAIL: the CSP seed must sit directly under 'if [[ ! -f ${snippet} ]]' — never overwrite the agent's copy"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "PASS: panel CSP snippet included, seeded before nginx -t, never overwritten"

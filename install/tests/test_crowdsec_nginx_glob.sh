#!/usr/bin/env bash
# install/tests/test_crowdsec_nginx_glob.sh — regression coverage for
# JAB-250: the CrowdSec nginx acquisition glob must match per-domain
# tenant logs, which the panel writes as <domain>-access.log (hyphen).
#
# The original glob was /var/log/nginx/*.access.log (dot before access),
# which matched ONLY infra logs and NONE of the 101 tenant sites on a
# real box — every log-based scenario (WordPress brute-force, http-dos,
# http-cve) was starved. This test pins:
#   1. install.sh globs BOTH *-access.log and *.access.log in the jabali
#      acquisition file.
#   2. The default-acquis narrowing sed produces both patterns, not the
#      dot-only one.
#   3. The glob pattern actually matches a realistic tenant filename and
#      excludes the jcache log (different format).
#
# Run from repo root:  bash install/tests/test_crowdsec_nginx_glob.sh
# Exit 0 = pass.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0

# --- 1. jabali acquisition file globs both spellings. ---
acquis_line="$(grep -n 'desired_nginx_acquis=' install.sh | head -1 || true)"
if [[ -z "$acquis_line" ]]; then
  echo "FAIL: desired_nginx_acquis assignment not found in install.sh"
  fail=1
else
  val="$(grep 'desired_nginx_acquis=' install.sh | head -1)"
  if ! grep -q '/var/log/nginx/\*-access.log' <<<"$val"; then
    echo "FAIL: jabali nginx acquis does NOT glob *-access.log (tenant logs) — JAB-250 regression"
    fail=1
  fi
  if ! grep -q '/var/log/nginx/\*.access.log' <<<"$val"; then
    echo "FAIL: jabali nginx acquis dropped *.access.log (infra logs)"
    fail=1
  fi
fi

# --- 2. Default-acquis narrowing sed emits both patterns. ---
if grep -q "sed -i 's|/var/log/nginx/\\\\\*\\\\.log|/var/log/nginx/\*\.access\.log|g'" install.sh; then
  echo "FAIL: default-acquis narrowing still collapses to dot-only *.access.log — JAB-250 regression"
  fail=1
fi
if ! grep -q '/var/log/nginx/\*-access.log' <<<"$(grep 'narrowing nginx glob' -A2 install.sh)"; then
  # best-effort: the replacement line should mention the hyphen pattern
  if ! grep -Eq "sed -i 's\|/var/log/nginx/\\\\\\*\\\\.log\|/var/log/nginx/\*-access\.log" install.sh; then
    echo "FAIL: default-acquis narrowing does not include *-access.log"
    fail=1
  fi
fi

# --- 3. Functional: the glob matches tenant + preview, excludes jcache. ---
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
touch "$tmp/example.com-access.log" \
      "$tmp/example.com-preview-access.log" \
      "$tmp/jabali-panel.access.log" \
      "$tmp/jcache-example.com.log" \
      "$tmp/example.com-error.log"

shopt -s nullglob
matched=("$tmp"/*-access.log "$tmp"/*.access.log)
shopt -u nullglob

have() { local n="$1" m; for m in "${matched[@]}"; do [[ "$(basename "$m")" == "$n" ]] && return 0; done; return 1; }

have "example.com-access.log"         || { echo "FAIL: tenant -access.log not matched"; fail=1; }
have "example.com-preview-access.log" || { echo "FAIL: preview -access.log not matched"; fail=1; }
have "jabali-panel.access.log"        || { echo "FAIL: infra .access.log not matched"; fail=1; }
if have "jcache-example.com.log"; then echo "FAIL: jcache log (different format) wrongly matched"; fail=1; fi
if have "example.com-error.log";  then echo "FAIL: error log wrongly matched"; fail=1; fi

if [[ "$fail" == 0 ]]; then
  echo "PASS: crowdsec nginx glob covers tenant + preview + infra, excludes jcache/error (JAB-250)"
fi
exit "$fail"

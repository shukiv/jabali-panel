#!/usr/bin/env bash
# install/tests/test_etc_hosts_loopback_strip.sh — regression coverage
# for 2026-06-04 vpsjournal.com incident: the install.sh strip of the
# `127.0.1.1 <hostname>` loopback shadow MUST run unconditionally,
# NOT gated on --hostname / JABALI_HOSTNAME.
#
# Why this matters: net.LookupHost respects /etc/hosts before DNS, so
# the M32 panel-cert routability gate sees 127.0.1.1 when the shadow
# is present, refuses to attempt LE issuance, and the panel never
# gets a real cert. Operators who pre-set the system hostname and
# run install.sh without --hostname were silently broken until 2026-06-04.
#
# This test does two things:
#   1. Static check that the strip block is OUTSIDE the
#      `if [[ -n "${JABALI_HOSTNAME:-}" ]]` block in install.sh.
#   2. Functional check that the strip regex deletes every 127.0.1.1
#      line shape while preserving 127.0.0.1, ::1, and public-IP rows.
#
# Run from repo root:
#     bash install/tests/test_etc_hosts_loopback_strip.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. Static check: strip lives outside the JABALI_HOSTNAME gate. ---

# Find absolute line numbers in install.sh of:
#   * the open of the JABALI_HOSTNAME conditional in prompt_server_settings
#   * the close `  fi` that immediately matches that open
#   * the first `sed -i ... 127.0.1.1` line that strips the shadow
#
# The strip MUST sit AFTER the matching `  fi` close. If it sits
# before, the strip is still gated and the bug is back.

prompt_fn_start=$(grep -n '^prompt_server_settings()' install.sh | head -n1 | cut -d: -f1 || true)
if [[ -z "$prompt_fn_start" ]]; then
  echo "FAIL: prompt_server_settings() not found in install.sh"
  fail=1
else
  gate_open=$(awk -v s="$prompt_fn_start" 'NR>=s && /if \[\[ -n "\$\{JABALI_HOSTNAME:-\}" \]\]; then/ {print NR; exit}' install.sh)
  strip_line=$(awk -v s="$prompt_fn_start" 'NR>=s && /127\.0\.1\.1/ {print NR; exit}' install.sh)
  # Match the gate's closing `  fi` (two-space indent) — the first one
  # at or after gate_open.
  gate_close=$(awk -v s="$gate_open" 'NR>=s && /^  fi$/ {print NR; exit}' install.sh)

  if [[ -z "$gate_open" || -z "$gate_close" || -z "$strip_line" ]]; then
    echo "FAIL: could not locate (gate_open=$gate_open, gate_close=$gate_close, strip_line=$strip_line)"
    fail=1
  # The requirement is that the strip is not INSIDE the gate. It can satisfy
  # that from either side — this originally asserted "after the gate closes",
  # but the strip was later moved to the top of the function instead, which is
  # equally unconditional. Asserting one arrangement rather than the property
  # made this test read a correct install.sh as a regression.
  elif [[ "$strip_line" -ge "$gate_open" && "$strip_line" -le "$gate_close" ]]; then
    echo "FAIL: /etc/hosts strip (line $strip_line) is inside the JABALI_HOSTNAME gate (lines $gate_open-$gate_close) — still gated, bug regressed"
    fail=1
  else
    echo "ok: strip at line $strip_line is unconditional (gate spans $gate_open-$gate_close)"
  fi
fi

# --- 2. Functional check: the regex strips every 127.0.1.1 shape. ---

tmp="$(mktemp)"
cat > "$tmp" <<'HOSTS'
127.0.0.1	localhost
127.0.1.1	vpsjournal.com
127.0.1.1	old-hostname.example  some-alias
127.0.1.1
::1		ip6-localhost ip6-loopback
208.84.101.36	vpsjournal.com	vpsjournal
HOSTS

sed -i '/^127\.0\.1\.1\([[:space:]]\|$\)/d' "$tmp"

remaining_loopback="$(grep -c '^127\.0\.1\.1' "$tmp" || true)"
if [[ "$remaining_loopback" -ne 0 ]]; then
  echo "FAIL: strip left $remaining_loopback 127.0.1.1 line(s) behind"
  fail=1
else
  echo "ok: every 127.0.1.1 line stripped"
fi

if ! grep -q '^127\.0\.0\.1[[:space:]]localhost$' "$tmp"; then
  echo "FAIL: strip removed 127.0.0.1 localhost line"
  fail=1
else
  echo "ok: 127.0.0.1 localhost preserved"
fi

if ! grep -q '^208\.84\.101\.36' "$tmp"; then
  echo "FAIL: strip removed the public-IP row"
  fail=1
else
  echo "ok: public-IP row preserved"
fi

if ! grep -q '^::1' "$tmp"; then
  echo "FAIL: strip removed the ::1 row"
  fail=1
else
  echo "ok: ::1 row preserved"
fi

rm -f "$tmp"

if [[ "$fail" -ne 0 ]]; then
  echo
  echo "FAIL: /etc/hosts loopback-shadow strip regressed"
  exit 1
fi

echo
echo "PASS: /etc/hosts 127.0.1.1 loopback-shadow strip is unconditional and correct"

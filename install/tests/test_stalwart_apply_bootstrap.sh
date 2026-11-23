#!/usr/bin/env bash
# install/tests/test_stalwart_apply_bootstrap.sh — a Stalwart that started
# before its config was written (bootstrap mode on :8080, temporary admin
# password) must not pass as ready, and the 401 its apply returns must not
# pass as an idempotent re-run.
#
# 1. _stalwart_apply_non_idempotent on stalwart-cli 1.0.14's real output.
# 2. _stalwart_admin_token_ok with curl stubbed: right/wrong token, and the
#    token never on curl's argv.
# 3. _stalwart_wait_jmap_port with curl stubbed.
# 4. Static: _install_stalwart_apply_plan checks the token and restarts.
#
# Run from repo root:
#     bash install/tests/test_stalwart_apply_bootstrap.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn() { awk -v n="$1" '$0 ~ "^"n"\\(\\)" {f=1} f{print} f&&/^\}/{exit}' install.sh; }
for f in _stalwart_apply_non_idempotent _stalwart_admin_token_ok _stalwart_wait_jmap_port; do
  if [[ -z "$(fn "$f")" ]]; then
    echo "FAIL: $f() not found in install.sh"
    exit 1
  fi
  eval "$(fn "$f")"
done

# 1. Apply outcomes, as stalwart-cli 1.0.14 prints them.
existing=$'Plan: 0 destroy, 0 update, 1 create, 0 upsert, 0 reconcile (1 objects)\n✗ create NetworkListener: NetworkListener: create failed for `smtp-25` (operation #1): error: primaryKeyViolation |   Properties: name\nDone: 0 destroyed, 0 updated, 0 created (1 failed)\nerror: apply completed with 1 failed operation(s)'
refused=$'warning: failed to refresh schema (authentication failed (HTTP 401)); using cached copy\nerror: authentication failed (HTTP 401)'
mixed=$'✗ create NetworkListener: create failed for `smtp-25` (operation #1): error: primaryKeyViolation\n✗ create Tracer: create failed (operation #2): error: invalidProperties\nerror: apply completed with 2 failed operation(s)'

out=$(_stalwart_apply_non_idempotent "$existing")
if [[ -n "$out" ]]; then
  echo "FAIL: only primaryKeyViolation must read as idempotent, got: $out"
  fail=1
fi
out=$(_stalwart_apply_non_idempotent "$refused")
if [[ "$out" != "error: authentication failed (HTTP 401)" ]]; then
  echo "FAIL: a 401 with no operation run must be reported, got: ${out:-<empty = idempotent>}"
  fail=1
fi
out=$(_stalwart_apply_non_idempotent "$mixed")
if [[ "$out" != *invalidProperties* || "$out" == *primaryKeyViolation* ]]; then
  echo "FAIL: a real per-operation failure must be reported alone, got: ${out:-<empty>}"
  fail=1
fi
out=$(_stalwart_apply_non_idempotent "")
if [[ -z "$out" ]]; then
  echo "FAIL: a failed apply with no output at all must be reported"
  fail=1
fi

# 2. Token check. The stub answers 200 only when the config on stdin
# carries the right user, and records its argv.
ARGV_LOG=$(mktemp)
curl() {
  echo "$*" >>"$ARGV_LOG"
  if grep -qx 'user = "admin:s3cret+tok/en="' <(cat); then printf 200; else printf 401; fi
}
if ! _stalwart_admin_token_ok 8080 's3cret+tok/en='; then
  echo "FAIL: the right admin token was refused"
  fail=1
fi
if _stalwart_admin_token_ok 8080 'bootstrap-temp-password'; then
  echo "FAIL: a refused admin token (bootstrap mode) passed"
  fail=1
fi
if grep -q 's3cret' "$ARGV_LOG"; then
  echo "FAIL: the admin token reached curl's argv"
  fail=1
fi
rm -f "$ARGV_LOG"

# 3. Port probe: 8446 refuses (000), 8080 answers.
curl() { case "$*" in *:8080/*) printf 200 ;; *) printf 000 ;; esac; }
out=$(_stalwart_wait_jmap_port 2)
if [[ "$out" != "8080 200 0" ]]; then
  echo "FAIL: probe should report '8080 200 0', got: ${out:-<nothing>}"
  fail=1
fi
sleep() { :; }
curl() { printf 000; }
if _stalwart_wait_jmap_port 2 >/dev/null; then
  echo "FAIL: probe with nothing listening should time out"
  fail=1
fi
unset -f curl sleep

# 4. Static: the apply checks the token on the port it found, restarts on a
# refusal, and categorizes through the helper.
plan_fn=$(fn _install_stalwart_apply_plan)
if ! grep -q '_stalwart_admin_token_ok "\$jmap_port" "\$admin_token"' <<<"$plan_fn"; then
  echo "FAIL: _install_stalwart_apply_plan does not check the admin token"
  fail=1
fi
# The restart must come inside the refusal branch, before its re-probe (the
# function's final restart further down does not count).
if ! awk '/if ! _stalwart_admin_token_ok/{f=1; next} f&&/_stalwart_wait_jmap_port/{exit} f&&/systemctl restart jabali-stalwart.service/{ok=1; exit} END{exit !ok}' <<<"$plan_fn"; then
  echo "FAIL: a refused admin token does not restart Stalwart"
  fail=1
fi
if ! grep -q '_stalwart_apply_non_idempotent "\$apply_out"' <<<"$plan_fn"; then
  echo "FAIL: the apply outcome is not categorized by _stalwart_apply_non_idempotent"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: test_stalwart_apply_bootstrap"
fi
exit "$fail"

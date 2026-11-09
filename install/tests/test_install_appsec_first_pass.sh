#!/usr/bin/env bash
# install/tests/test_install_appsec_first_pass.sh — a fresh install's first
# AppSec pass logs that it waits for the panel binary instead of warning.
#
# main() runs install_crowdsec_appsec twice: before build_backend, when
# /usr/local/bin/jabali-panel does not exist yet, and again after it, when the
# config is rendered and the listener comes up. The first pass printed three
# warnings on every fresh install ("appsec render-config failed: ... No such
# file or directory", "appsec config not present yet", "listener did not
# appear on :7422") although the second pass then made AppSec live.
#
# Source-level:
# 1. The render step checks for the binary first and marks the pass deferred;
#    a render that fails with the binary present still warns.
# 2. The acquis and listener warnings are skipped on a deferred pass.
# 3. main() still runs install_crowdsec_appsec again after build_backend —
#    without that second call, deferring would leave AppSec off.
#
# Run from repo root:
#     bash install/tests/test_install_appsec_first_pass.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn() { awk -v n="$1" '$0 == n "() {" {p=1} p {print} p && /^}$/ {exit}' install.sh; }

# Captured first: `fn | grep -q` under pipefail fails when grep exits early
# and awk, still printing a long function, takes SIGPIPE.
body=$(fn install_crowdsec_appsec)
if [[ -z "$body" ]]; then
  echo "FAIL: install_crowdsec_appsec not defined in install.sh"
  exit 1
fi

# --- 1. Binary check before the render; a real failure still warns. ---
check_line=$(grep -n 'if \[\[ ! -x /usr/local/bin/jabali-panel \]\]; then' <<<"$body" | head -1 | cut -d: -f1)
render_line=$(grep -n 'jabali-panel appsec render-config --reconcile' <<<"$body" | head -1 | cut -d: -f1)
if [[ -z "$check_line" || -z "$render_line" || "$check_line" -ge "$render_line" ]]; then
  echo "FAIL: the render step does not check for the panel binary before running it"
  fail=1
fi
if ! sed -n "${check_line:-1},$(( ${check_line:-1} + 3 ))p" <<<"$body" | grep -q '_appsec_deferred=1'; then
  echo "FAIL: a missing panel binary does not mark the pass deferred"
  fail=1
fi
if ! grep -q '_warn "jabali-panel appsec render-config failed' <<<"$body"; then
  echo "FAIL: a render that fails with the binary present no longer warns"
  fail=1
fi

# --- 2. The acquis and listener warnings are skipped when deferred. ---
for msg in 'appsec config not present yet' 'CrowdSec AppSec listener did not appear'; do
  line=$(grep -n "_warn \"$msg" <<<"$body" | head -1 | cut -d: -f1)
  if [[ -z "$line" ]]; then
    echo "FAIL: the warning '$msg' is gone (it must stay for a non-deferred pass)"
    fail=1
    continue
  fi
  guard=$(sed -n "$(( line > 4 ? line - 4 : 1 )),$(( line - 1 ))p" <<<"$body")
  if ! grep -q '_appsec_deferred' <<<"$guard"; then
    echo "FAIL: the warning '$msg' is not skipped on a deferred pass"
    fail=1
  fi
done

# --- 3. main() runs it again after build_backend. ---
main_body=$(fn main)
build_line=$(grep -n '^  build_backend$' <<<"$main_body" | head -1 | cut -d: -f1)
second=$(sed -n "${build_line:-1},\$p" <<<"$main_body" | grep -c 'run_if_module security install_crowdsec_appsec')
if [[ -z "$build_line" || "$second" -lt 1 ]]; then
  echo "FAIL: main() does not run install_crowdsec_appsec after build_backend"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: the first AppSec pass waits for the panel binary without warning"
fi
exit "$fail"

#!/usr/bin/env bash
# install/tests/test_secrets_rotate_not_automated.sh — JAB-357 criterion 7.
#
# `jabali secrets rotate …` is an OPERATOR CEREMONY: it changes live DB / Redis
# credentials and restarts services, one host at a time. It must NEVER be
# invoked automatically — not by install.sh, not by the converger
# (provision_new_software), not by the 04:30 fleet auto-update — because
# auto-rotating fleet secrets unprompted is exactly the outage this ticket
# exists to avoid. This guard fails if any automatic caller appears.
#
# It also asserts the tooling is registered and the runbook exists, so a
# rename that silently drops a rotate subcommand is caught.
#
# Static, source-only (no binary / DB / box needed) — runs in CI.
#     bash install/tests/test_secrets_rotate_not_automated.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
note() { printf '  %s\n' "$1"; }

# 1. install.sh must not invoke the rotation tool anywhere. Any `secrets rotate`
#    or `jabali secrets` call in install.sh means the fleet would rotate on
#    install/update — a critical regression.
if grep -nE '(jabali|secrets)[[:space:]]+secrets?[[:space:]]*rotate|secrets[[:space:]]+rotate' install.sh >/dev/null 2>&1; then
  echo "FAIL: install.sh invokes 'secrets rotate' — rotation must be operator-only, never automated:"
  grep -nE 'secrets[[:space:]]+rotate' install.sh | sed 's/^/    /'
  fail=1
else
  note "OK: install.sh never invokes 'secrets rotate' (explicit-invoke only)"
fi

# 2. The rotate subcommands are registered under the `secrets` group.
CMD=panel-api/cmd/server/secrets_rotate_cmd.go
if [[ ! -f "$CMD" ]]; then
  echo "FAIL: $CMD missing"
  fail=1
else
  for want in newRotateDBAppUserCmd newRotateJWTCmd newRotateRedisPanelTokenCmd newRotatePdnsCmd newRotateAllCmd; do
    if ! grep -q "$want" "$CMD"; then
      echo "FAIL: rotate subcommand $want not registered in $CMD"
      fail=1
    fi
  done
  # The group must be wired into the root command.
  if ! grep -q 'newSecretsCmd()' panel-api/cmd/server/root.go; then
    echo "FAIL: newSecretsCmd() not added to the root command"
    fail=1
  fi
  [[ $fail -eq 0 ]] && note "OK: db-app-user / jwt / redis-panel-token / pdns / all registered under 'secrets'"
fi

# 3. Every rotate rewrites secrets through the mode-preserving primitive, never
#    a raw os.WriteFile / os.Create that could loosen a 0640 secret to 0644.
if grep -nE 'os\.(WriteFile|Create)\(' "$CMD" >/dev/null 2>&1; then
  echo "FAIL: $CMD uses os.WriteFile/os.Create directly — secrets must go through atomicRewritePreserving:"
  grep -nE 'os\.(WriteFile|Create)\(' "$CMD" | sed 's/^/    /'
  fail=1
else
  note "OK: rewrites go through atomicRewritePreserving (mode/owner preserved)"
fi

# 4. The operator runbook exists.
if [[ ! -f docs/secret-rotation.md ]]; then
  echo "FAIL: docs/secret-rotation.md (operator runbook) missing"
  fail=1
else
  note "OK: docs/secret-rotation.md present"
fi

if [[ $fail -ne 0 ]]; then
  echo "test_secrets_rotate_not_automated: FAILED"
  exit 1
fi
echo "test_secrets_rotate_not_automated: PASSED"

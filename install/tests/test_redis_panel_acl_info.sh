#!/usr/bin/env bash
# install/tests/test_redis_panel_acl_info.sh — regression coverage for the
# jabali_panel Redis user being unable to run INFO.
#
# INFO is in @dangerous, and the panel's ACL line was
# `+@all -@dangerous +acl +@connection`. Every INFO panel-api sent (the
# WordPress cache diagnostic's evicted_keys warning, the admin hit ratio and
# used_memory) came back NOPERM, the error was dropped, and the numbers never
# showed on any box.
#
# Asserts install.sh ships:
#   1. +info on the jabali_panel ACL line fresh installs write.
#   2. A converge step for existing hosts, called from install_redis_acl's
#      fast path (the path every already-provisioned host takes on update).
#   3. The panel token passed through REDISCLI_AUTH, never argv.
#   4. When redis-server is installed: the converge step run against a real
#      Redis with the old ACL grants INFO, keeps a tenant user in users.acl,
#      still denies CONFIG, is a no-op on a second run, and grants nothing on
#      a wrong token.
#
# Run from repo root:
#     bash install/tests/test_redis_panel_acl_info.sh
#
# Exit 0 = pass.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# --- 1. Fresh installs: the ACL heredoc grants +info. ---
panel_line=$(grep -E '^user jabali_panel on >\$\{panel_token\} ' install.sh || true)
if [[ -z "$panel_line" ]]; then
  echo "FAIL: jabali_panel ACL line not found in install.sh"
  fail=1
elif ! grep -qE '(^| )\+info( |$)' <<<"$panel_line"; then
  echo "FAIL: jabali_panel ACL line lacks +info: $panel_line"
  fail=1
fi

# --- 2. Existing hosts: the fast path runs the converge step. ---
fn_src=$(awk '/^converge_redis_panel_info_acl\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: converge_redis_panel_info_acl not defined in install.sh"
  exit 1
fi
acl_fn=$(awk '/^install_redis_acl\(\) \{$/,/^\}$/' install.sh)
fast_path=$(awk '/^  if getent group jabali-redis-clients/,/^  fi$/' <<<"$acl_fn")
if ! grep -q 'converge_redis_panel_info_acl' <<<"$fast_path"; then
  echo "FAIL: install_redis_acl's fast path does not call converge_redis_panel_info_acl"
  fail=1
fi

# --- 3. Token via environment only. ---
if grep -q -- '--pass' <<<"$fn_src"; then
  echo "FAIL: converge_redis_panel_info_acl puts the token in argv (--pass)"
  fail=1
fi
if ! grep -q 'REDISCLI_AUTH=' <<<"$fn_src"; then
  echo "FAIL: converge_redis_panel_info_acl does not use REDISCLI_AUTH"
  fail=1
fi

# --- 4. Behaviour against a real Redis, when one is installed. ---
if ! command -v redis-server >/dev/null 2>&1 || ! command -v redis-cli >/dev/null 2>&1; then
  echo "SKIP: redis-server not installed; behaviour checks not run"
else
  work=$(mktemp -d /tmp/jabali-redis-acl.XXXXXX)
  sock="$work/r.sock"
  pid=""
  cleanup() {
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
    rm -rf "$work"
  }
  trap cleanup EXIT

  # The pre-fix ACL (default already locked, as on a provisioned host) plus a
  # tenant user saved there by panel-api.
  cat >"$work/users.acl" <<'ACL'
user default off nopass ~* resetchannels -@all
user jabali_panel on >tok123 ~jabali:* ~automation:* resetchannels +@all -@dangerous +acl +@connection
user wp_alice on >alicepw ~jc:alice:* resetchannels +@read +@write +@keyspace +@connection -@dangerous
ACL
  redis-server --port 0 --unixsocket "$sock" --aclfile "$work/users.acl" \
    --dir "$work" --save '' --appendonly no --daemonize yes \
    --pidfile "$work/r.pid" --logfile "$work/r.log"
  for _ in 1 2 3 4 5 6 7 8 9 10; do [[ -S "$sock" ]] && break; sleep 0.2; done
  pid=$(cat "$work/r.pid" 2>/dev/null || true)

  printf 'JABALI_REDIS_PANEL_TOKEN=tok123\n' >"$work/panel.env"
  _ok()   { :; }
  _warn() { echo "warn: $*" >>"$work/warn.log"; }
  ENV_FILE="$work/panel.env"
  eval "${fn_src//\/run\/redis\/redis.sock/$sock}"

  # Capture whole replies before matching: `redis-cli | grep -q` can SIGPIPE
  # redis-cli, and under pipefail that reads as a failed match.
  as_panel() { REDISCLI_AUTH=tok123 redis-cli -s "$sock" --user jabali_panel --no-auth-warning "$@" 2>&1 || true; }
  first_line() { local o; o=$(as_panel "$@"); printf '%s\n' "${o%%$'\n'*}"; }

  if [[ "$(first_line INFO server)" != NOPERM* ]]; then
    echo "FAIL: setup: the old ACL should refuse INFO"
    fail=1
  fi

  # Wrong token: must grant nothing.
  printf 'JABALI_REDIS_PANEL_TOKEN=wrong\n' >"$work/panel.env"
  converge_redis_panel_info_acl
  if [[ "$(first_line INFO server)" != NOPERM* ]]; then
    echo "FAIL: a wrong token still changed the jabali_panel ACL"
    fail=1
  fi
  if [[ ! -s "$work/warn.log" ]]; then
    echo "FAIL: a wrong token produced no warning"
    fail=1
  fi

  printf 'JABALI_REDIS_PANEL_TOKEN=tok123\n' >"$work/panel.env"
  converge_redis_panel_info_acl
  stats=$(as_panel INFO stats)
  if [[ "$stats" != *$'\n'evicted_keys:* ]]; then
    echo "FAIL: jabali_panel still cannot read evicted_keys after the converge step"
    fail=1
  fi
  # Persisted: restart Redis from users.acl. Checked by behaviour, not by
  # grepping the file: ACL SAVE writes a version-specific canonical form
  # (Redis 7.0 expands the categories and never prints "+info").
  kill "$pid" 2>/dev/null || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do [[ -S "$sock" ]] || break; sleep 0.2; done
  redis-server --port 0 --unixsocket "$sock" --aclfile "$work/users.acl" \
    --dir "$work" --save '' --appendonly no --daemonize yes \
    --pidfile "$work/r.pid" --logfile "$work/r.log"
  for _ in 1 2 3 4 5 6 7 8 9 10; do [[ -S "$sock" ]] && break; sleep 0.2; done
  pid=$(cat "$work/r.pid" 2>/dev/null || true)
  if [[ "$(first_line INFO server)" != "# Server"* ]]; then
    echo "FAIL: the +info grant did not survive a Redis restart (not saved to users.acl)"
    fail=1
  fi
  if [[ "$(as_panel ACL USERS)" != *wp_alice* ]]; then
    echo "FAIL: the tenant user was lost from users.acl"
    fail=1
  fi
  if [[ "$(first_line CONFIG GET maxmemory)" != NOPERM* ]]; then
    echo "FAIL: jabali_panel gained CONFIG; only INFO should be added"
    fail=1
  fi

  before=$(cat "$work/users.acl")
  converge_redis_panel_info_acl
  if [[ "$(cat "$work/users.acl")" != "$before" ]]; then
    echo "FAIL: a second run rewrote users.acl"
    fail=1
  fi
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi
echo "PASS: jabali_panel Redis user may run INFO on fresh and existing hosts"

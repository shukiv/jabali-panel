#!/usr/bin/env bash
# install/tests/test_stalwart_directory_converger_gate.sh — GH #1816 / ADR-0170.
#
# converge_stalwart_directory_queries writes the SQL x:Directory queries. The
# ownership filters read domains.ownership_status (migration 000311), and on
# `jabali update` the converger first runs BEFORE `migrate up`. A query naming
# a column that does not exist yet fails every lookup, so on such a box the
# function must keep the pre-#1816 queries; once the column exists it must
# write the filtered ones (queryLogin included) and reload Stalwart.
#
# Behavioural: extracts the function from install.sh and runs it against a
# fake stalwart-cli and a fake mariadb.
#
# Run from repo root:
#     bash install/tests/test_stalwart_directory_converger_gate.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
fn_src=$(awk '/^converge_stalwart_directory_queries\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: converge_stalwart_directory_queries() not found in install.sh"
  exit 1
fi
eval "$fn_src"

_log() { :; }
_ok() { :; }
_warn() { :; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Fake stalwart-cli: one SQL directory; records the update patch and actions.
cat >"$tmp/stalwart-cli" <<'EOF'
#!/usr/bin/env bash
case "$1 $2" in
  "query Directory") echo '{"@type":"Sql","id":"dir1"}' ;;
  "update Directory")
    [[ -n "${FAKE_UPDATE_FAILS:-}" ]] && exit 1
    printf '%s' "$5" >"$FAKE_DIR/patch.json" ;;
  "create Action") printf '%s\n' "$4" >>"$FAKE_DIR/actions" ;;
esac
EOF
chmod +x "$tmp/stalwart-cli"
export JABALI_STALWART_CLI="$tmp/stalwart-cli" FAKE_DIR="$tmp"

# Fake mariadb: answers the information_schema probe with $FAKE_COLUMN.
mariadb() { echo "${FAKE_COLUMN:-0}"; }

field() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$tmp/patch.json" "$1"; }

run_case() {
  rm -f "$tmp/patch.json" "$tmp/actions"
  FAKE_COLUMN="$1" converge_stalwart_directory_queries 8446 token
}

# 1. migrated box: every query filters on ownership, and Stalwart is reloaded.
run_case 1
for f in queryLogin queryRecipient queryEmailAliases; do
  if [[ "$(field "$f")" != *"ownership_status = 'verified'"* ]]; then
    echo "FAIL: migrated box: $f has no ownership filter"
    fail=1
  fi
done
if ! grep -q '"@type":"ReloadSettings"' "$tmp/actions" 2>/dev/null; then
  echo "FAIL: migrated box: Stalwart was not told to reload the new queries"
  fail=1
fi

# 2. box before migration 000311: no query may name the missing column.
run_case 0
for f in queryLogin queryRecipient queryEmailAliases; do
  if [[ "$(field "$f")" == *"ownership_status"* ]]; then
    echo "FAIL: unmigrated box: $f names domains.ownership_status, which does not exist yet — every lookup would fail"
    fail=1
  fi
done

# 1 and 2: with or without the column, the rules that do not depend on it stay:
# a suspended owner's mailboxes cannot sign in, an alias never takes a
# mailbox's address, and postmaster@ falls back to the admin (ADR-0110).
for col in 1 0; do
  run_case "$col"
  if [[ "$(field queryLogin)" != *"u.suspended = 1"* ]]; then
    echo "FAIL: column=$col: queryLogin lets a suspended owner's mailboxes sign in"
    fail=1
  fi
  for f in queryRecipient queryEmailAliases; do
    if [[ "$(field "$f")" != *"NOT EXISTS (SELECT 1 FROM mailboxes xo WHERE xo.domain_id = f.domain_id AND xo.local_part = f.local_part)"* ]]; then
      echo "FAIL: column=$col: $f lets an alias take a mailbox's address"
      fail=1
    fi
    if [[ "$(field "$f")" != *"'postmaster@', pd.name"* ]]; then
      echo "FAIL: column=$col: $f lost the postmaster fallback (ADR-0110)"
      fail=1
    fi
  done
done

# 3. a failed Directory update is reported, so `jabali update` can say so.
if FAKE_UPDATE_FAILS=1 FAKE_COLUMN=1 converge_stalwart_directory_queries 8446 token; then
  echo "FAIL: a failed Directory update returned success"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "PASS: the Directory converger writes the ownership filters only once the column exists, and reloads Stalwart"
fi
exit "$fail"

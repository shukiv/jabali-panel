#!/usr/bin/env bash
# Test: Staging Directory Patterns
# Verifies that collectors produce files that restorers and the inventory
# parser expect. This catches mismatches between what's written and what's read.
#
# Usage: bash tests/bash/test_staging_patterns.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

PASS=0
FAIL=0
TOTAL=0

pass() { PASS=$((PASS + 1)); TOTAL=$((TOTAL + 1)); printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail() { FAIL=$((FAIL + 1)); TOTAL=$((TOTAL + 1)); printf "  \033[31m✗\033[0m %s\n" "$1"; }
section() { echo ""; printf "\033[1m%s\033[0m\n" "$1"; }

# ─────────────────────────────────────────────────────────────
section "1. Metadata: collector writes account.json, restorer reads it"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/metadata.sh"
restorer="$PROJECT_DIR/lib/restorers/metadata.sh"

# Collector writes to metadata/account.json
if grep -q 'account.json' "$collector"; then
    pass "metadata collector writes account.json"
else
    fail "metadata collector does NOT write account.json"
fi

# Restorer reads metadata/account.json
if grep -q 'account.json' "$restorer"; then
    pass "metadata restorer reads account.json"
else
    fail "metadata restorer does NOT read account.json"
fi

# Collector writes domains array
if grep -q '"domains"' "$collector"; then
    pass "metadata collector includes domains in JSON"
else
    fail "metadata collector does NOT include domains"
fi

# Restorer reads domains from account.json
if grep -q '"domain"' "$restorer"; then
    pass "metadata restorer parses domains"
else
    fail "metadata restorer does NOT parse domains"
fi

# ─────────────────────────────────────────────────────────────
section "2. MySQL: collector writes .sql.gz, restorer reads .sql.gz"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/mysql.sh"
restorer="$PROJECT_DIR/lib/restorers/mysql.sh"

if grep -q '.sql.gz' "$collector"; then
    pass "mysql collector writes .sql.gz dumps"
else
    fail "mysql collector does NOT write .sql.gz"
fi

if grep -q '.sql.gz' "$restorer"; then
    pass "mysql restorer reads .sql.gz dumps"
else
    fail "mysql restorer does NOT read .sql.gz"
fi

# users.txt roundtrip
if grep -q 'users.txt' "$collector"; then
    pass "mysql collector writes users.txt"
else
    fail "mysql collector does NOT write users.txt"
fi

if grep -q 'users.txt' "$restorer"; then
    pass "mysql restorer reads users.txt"
else
    fail "mysql restorer does NOT read users.txt"
fi

# grants.sql roundtrip
if grep -q 'grants.sql' "$collector"; then
    pass "mysql collector writes grants.sql"
else
    fail "mysql collector does NOT write grants.sql"
fi

if grep -q 'grants.sql' "$restorer"; then
    pass "mysql restorer reads grants.sql"
else
    fail "mysql restorer does NOT read grants.sql"
fi

# ─────────────────────────────────────────────────────────────
section "3. PostgreSQL: collector writes .dump, restorer reads .dump"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/postgres.sh"
restorer="$PROJECT_DIR/lib/restorers/postgres.sh"

if grep -q '.dump' "$collector"; then
    pass "postgres collector writes .dump files"
else
    fail "postgres collector does NOT write .dump"
fi

if grep -q '.dump' "$restorer"; then
    pass "postgres restorer reads .dump files"
else
    fail "postgres restorer does NOT read .dump"
fi

# role.sql roundtrip
if grep -q 'role.sql' "$collector"; then
    pass "postgres collector writes role.sql"
else
    fail "postgres collector does NOT write role.sql"
fi

if grep -q 'role.sql' "$restorer"; then
    pass "postgres restorer reads role.sql"
else
    fail "postgres restorer does NOT read role.sql"
fi

# ─────────────────────────────────────────────────────────────
section "4. DNS: collector writes .json zones, restorer reads them"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/dns.sh"
restorer="$PROJECT_DIR/lib/restorers/dns.sh"

if grep -q '.json' "$collector"; then
    pass "dns collector writes .json zone files"
else
    fail "dns collector does NOT write .json"
fi

if grep -q '.json' "$restorer"; then
    pass "dns restorer reads .json zone files"
else
    fail "dns restorer does NOT read .json"
fi

# ─────────────────────────────────────────────────────────────
section "5. SSL: collector writes cert/key/ca-bundle, restorer reads them"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/ssl.sh"
restorer="$PROJECT_DIR/lib/restorers/ssl.sh"

for file in cert.pem privkey.pem ca-bundle.pem meta.json; do
    if grep -q "$file" "$collector"; then
        pass "ssl collector writes $file"
    else
        fail "ssl collector does NOT write $file"
    fi

    if grep -q "$file" "$restorer"; then
        pass "ssl restorer reads $file"
    else
        fail "ssl restorer does NOT read $file"
    fi
done

# ─────────────────────────────────────────────────────────────
section "6. Email: collector writes domain/mailbox JSON, restorer reads them"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/email.sh"
restorer="$PROJECT_DIR/lib/restorers/email.sh"

if grep -q 'domains/' "$collector"; then
    pass "email collector writes domains/ directory"
else
    fail "email collector does NOT write domains/"
fi

if grep -q '.json' "$restorer"; then
    pass "email restorer reads .json files"
else
    fail "email restorer does NOT read .json"
fi

# ─────────────────────────────────────────────────────────────
section "7. Nginx: collector writes .conf, restorer reads .conf"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/nginx.sh"
restorer="$PROJECT_DIR/lib/restorers/nginx.sh"

if grep -q '.conf' "$collector"; then
    pass "nginx collector writes .conf files"
else
    fail "nginx collector does NOT write .conf"
fi

if grep -q '.conf' "$restorer"; then
    pass "nginx restorer reads .conf files"
else
    fail "nginx restorer does NOT read .conf"
fi

# ─────────────────────────────────────────────────────────────
section "8. PHP: collector writes .conf pool configs, restorer reads them"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/php.sh"
restorer="$PROJECT_DIR/lib/restorers/php.sh"

if grep -q '.conf' "$collector"; then
    pass "php collector writes .conf pool files"
else
    fail "php collector does NOT write .conf"
fi

if grep -q '.conf' "$restorer"; then
    pass "php restorer reads .conf pool files"
else
    fail "php restorer does NOT read .conf"
fi

# ─────────────────────────────────────────────────────────────
section "9. Cron: collector writes jobs.json, restorer reads it"
# ─────────────────────────────────────────────────────────────

collector="$PROJECT_DIR/lib/collectors/cron.sh"
restorer="$PROJECT_DIR/lib/restorers/cron.sh"

if grep -q 'jobs.json' "$collector"; then
    pass "cron collector writes jobs.json"
else
    fail "cron collector does NOT write jobs.json"
fi

if grep -q 'jobs.json' "$restorer"; then
    pass "cron restorer reads jobs.json"
else
    fail "cron restorer does NOT read jobs.json"
fi

# Also check system crontab roundtrip
if grep -q 'crontab' "$collector"; then
    pass "cron collector exports system crontab"
else
    fail "cron collector does NOT export crontab"
fi

if grep -q 'crontab' "$restorer"; then
    pass "cron restorer imports system crontab"
else
    fail "cron restorer does NOT import crontab"
fi

# ─────────────────────────────────────────────────────────────
section "10. Inventory ↔ Collector File Patterns Match"
# The inventory parser must detect the same file extensions
# that collectors produce
# ─────────────────────────────────────────────────────────────

agent="$PROJECT_DIR/panel/agent/jabali-backup.php"

# MySQL: .sql.gz
if grep -q "str_ends_with.*\.sql\.gz" "$agent"; then
    pass "inventory detects .sql.gz (mysql)"
else
    fail "inventory does NOT detect .sql.gz"
fi

# PostgreSQL: .dump
if grep -q "str_ends_with.*\.dump" "$agent"; then
    pass "inventory detects .dump (postgres)"
else
    fail "inventory does NOT detect .dump"
fi

# DNS: .json
if grep -q "str_ends_with.*\.json" "$agent"; then
    pass "inventory detects .json (dns/email)"
else
    fail "inventory does NOT detect .json"
fi

# Nginx: .conf
if grep -q "str_ends_with.*\.conf" "$agent"; then
    pass "inventory detects .conf (nginx/php)"
else
    fail "inventory does NOT detect .conf"
fi

# Metadata: account.json
if grep -q "account\.json" "$agent"; then
    pass "inventory dumps account.json for metadata"
else
    fail "inventory does NOT dump account.json"
fi

# MySQL: users.txt
if grep -q "users\.txt" "$agent"; then
    pass "inventory dumps users.txt for mysql"
else
    fail "inventory does NOT dump users.txt"
fi

# ─────────────────────────────────────────────────────────────
section "11. Restore Order Dependencies"
# Metadata must run before files, files before configs
# ─────────────────────────────────────────────────────────────

cli="$PROJECT_DIR/bin/jabali-backup"

# Extract line numbers for each restorer call in cmd_restore
get_line() {
    grep -n "$1" "$cli" | grep -v "dry_run\|echo\|#" | head -1 | cut -d: -f1
}

meta_line=$(get_line "restore_metadata " || echo 0)
domains_line=$(get_line "restore_metadata_domains" || echo 0)
files_line=$(get_line "restore_files " || echo 0)
dns_line=$(get_line "restore_dns " || echo 0)
ssl_line=$(get_line "restore_ssl " || echo 0)
nginx_line=$(get_line "restore_nginx " || echo 0)
mysql_line=$(get_line "restore_mysql " || echo 0)
postgres_line=$(get_line "restore_postgres " || echo 0)
cron_line=$(get_line "restore_cron " || echo 0)

if [[ "$meta_line" -lt "$domains_line" ]] 2>/dev/null; then
    pass "metadata runs before domains"
else
    fail "metadata must run before domains"
fi

if [[ "$domains_line" -lt "$files_line" ]] 2>/dev/null; then
    pass "domains created before files restored"
else
    fail "domains must be created before files"
fi

if [[ "$files_line" -lt "$nginx_line" ]] 2>/dev/null; then
    pass "files restored before nginx config"
else
    fail "files should be restored before nginx"
fi

if [[ "$files_line" -lt "$mysql_line" ]] 2>/dev/null; then
    pass "files restored before mysql"
else
    fail "files should be restored before mysql"
fi

if [[ "$dns_line" -lt "$nginx_line" ]] 2>/dev/null; then
    pass "dns restored before nginx"
else
    fail "dns should be restored before nginx"
fi

# ─────────────────────────────────────────────────────────────
section "12. Force Mode Handling"
# Each restorer should handle --force flag
# ─────────────────────────────────────────────────────────────

for comp in mysql postgres dns ssl email nginx cron; do
    restorer="$PROJECT_DIR/lib/restorers/${comp}.sh"
    if grep -q 'force' "$restorer" 2>/dev/null; then
        pass "${comp} restorer handles force flag"
    else
        fail "${comp} restorer does NOT handle force flag"
    fi
done

# ─────────────────────────────────────────────────────────────
# Summary
# ─────────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════"
printf "Total: %d  " "$TOTAL"
printf "\033[32mPass: %d\033[0m  " "$PASS"
if [[ "$FAIL" -gt 0 ]]; then
    printf "\033[31mFail: %d\033[0m" "$FAIL"
else
    printf "Fail: 0"
fi
echo ""
echo "════════════════════════════════════════"

exit "$FAIL"

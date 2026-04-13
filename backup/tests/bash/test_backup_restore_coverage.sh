#!/usr/bin/env bash
# Test: Backup/Restore Coverage
# Verifies that every backup component has matching collector, restorer,
# inventory parser, panel wizard tab, and CLI restore handling.
#
# Usage: bash tests/bash/test_backup_restore_coverage.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

PASS=0
FAIL=0
TOTAL=0

pass() {
    PASS=$((PASS + 1))
    TOTAL=$((TOTAL + 1))
    printf "  \033[32m✓\033[0m %s\n" "$1"
}

fail() {
    FAIL=$((FAIL + 1))
    TOTAL=$((TOTAL + 1))
    printf "  \033[31m✗\033[0m %s\n" "$1"
}

section() {
    echo ""
    printf "\033[1m%s\033[0m\n" "$1"
}

# ─── All components that should be backed up and restorable ───
# wordpress is excluded — it's backed up via files collector, no separate restorer needed
COMPONENTS=(metadata files mysql postgres dns ssl email nginx php cron stalwart)

# ─────────────────────────────────────────────────────────────
section "1. Collector ↔ Restorer Symmetry"
# Every component must have both a collector and a restorer
# ─────────────────────────────────────────────────────────────

for comp in "${COMPONENTS[@]}"; do
    collector="$PROJECT_DIR/lib/collectors/${comp}.sh"
    restorer="$PROJECT_DIR/lib/restorers/${comp}.sh"

    if [[ -f "$collector" ]]; then
        pass "collector exists: ${comp}.sh"
    else
        fail "collector MISSING: ${comp}.sh"
    fi

    if [[ -f "$restorer" ]]; then
        pass "restorer exists: ${comp}.sh"
    else
        fail "restorer MISSING: ${comp}.sh"
    fi
done

# ─────────────────────────────────────────────────────────────
section "2. Collector Functions Are Defined"
# Each collector file must define a collect_<name> function
# ─────────────────────────────────────────────────────────────

for comp in "${COMPONENTS[@]}"; do
    collector="$PROJECT_DIR/lib/collectors/${comp}.sh"
    [[ ! -f "$collector" ]] && continue
    if grep -qP "^collect_${comp}\(\)" "$collector" 2>/dev/null; then
        pass "collect_${comp}() defined"
    else
        fail "collect_${comp}() NOT defined in ${comp}.sh"
    fi
done

# ─────────────────────────────────────────────────────────────
section "3. Restorer Functions Are Defined"
# Each restorer file must define a restore_<name> function
# ─────────────────────────────────────────────────────────────

for comp in "${COMPONENTS[@]}"; do
    restorer="$PROJECT_DIR/lib/restorers/${comp}.sh"
    [[ ! -f "$restorer" ]] && continue
    if grep -qP "^restore_${comp}\(\)" "$restorer" 2>/dev/null; then
        pass "restore_${comp}() defined"
    else
        fail "restore_${comp}() NOT defined in ${comp}.sh"
    fi
done

# ─────────────────────────────────────────────────────────────
section "4. CLI Backup Command Calls Every Collector"
# bin/jabali-backup must call collect_<name> for each component
# ─────────────────────────────────────────────────────────────

cli="$PROJECT_DIR/bin/jabali-backup"

for comp in "${COMPONENTS[@]}"; do
    if grep -q "collect_${comp}" "$cli" 2>/dev/null; then
        pass "CLI calls collect_${comp}"
    else
        fail "CLI does NOT call collect_${comp}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "5. CLI Restore Command Calls Every Restorer"
# cmd_restore() must call restore_<name> for each component
# ─────────────────────────────────────────────────────────────

for comp in "${COMPONENTS[@]}"; do
    if grep -q "restore_${comp}" "$cli" 2>/dev/null; then
        pass "CLI calls restore_${comp}"
    else
        fail "CLI does NOT call restore_${comp}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "6. Inventory Parser Handles Every Component"
# jbSnapshotInventory() must have a case for each component
# ─────────────────────────────────────────────────────────────

agent="$PROJECT_DIR/panel/agent/jabali-backup.php"

for comp in "${COMPONENTS[@]}"; do
    # Check that the inventory array has this component key
    if grep -qP "'${comp}'\\s*=>" "$agent" 2>/dev/null; then
        pass "inventory has key: ${comp}"
    else
        fail "inventory MISSING key: ${comp}"
    fi
done

# Also check the switch/case handles each staging component
for comp in "${COMPONENTS[@]}"; do
    # metadata and files are handled differently (not in staging switch)
    [[ "$comp" == "files" ]] && continue
    if grep -qP "case '${comp}':" "$agent" 2>/dev/null; then
        pass "inventory switch handles: ${comp}"
    else
        fail "inventory switch MISSING case: ${comp}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "7. Restore Wizard Has Tab for Every Component"
# The restore wizard in Backups.php must handle each component
# ─────────────────────────────────────────────────────────────

wizard="$PROJECT_DIR/panel/filament/pages/Backups.php"

# Check wizard tabs (inventory-driven tabs)
for comp in "${COMPONENTS[@]}"; do
    # metadata → 'restore_metadata', files → 'restore_files', etc.
    restore_key="restore_${comp}"
    if grep -q "$restore_key" "$wizard" 2>/dev/null; then
        pass "wizard handles: ${restore_key}"
    else
        fail "wizard MISSING: ${restore_key}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "8. Wizard Action Sends Every Component to CLI"
# The action handler must add each component to the CLI args
# ─────────────────────────────────────────────────────────────

for comp in "${COMPONENTS[@]}"; do
    # Check the action handler adds this component
    if grep -qP "\\\$components\[\]\\s*=\\s*'${comp}'" "$wizard" 2>/dev/null; then
        pass "wizard action sends: ${comp}"
    else
        fail "wizard action does NOT send: ${comp}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "9. Multi-Account Restore Options Include Every Component"
# getRestoreComponentOptions() must list all components
# ─────────────────────────────────────────────────────────────

for comp in "${COMPONENTS[@]}"; do
    if grep -qP "'${comp}'\\s*=>\\s*__\(" "$wizard" 2>/dev/null; then
        pass "getRestoreComponentOptions has: ${comp}"
    else
        fail "getRestoreComponentOptions MISSING: ${comp}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "10. Agent Route Accepts Every Component"
# jbRestore() validComponents must include each component
# ─────────────────────────────────────────────────────────────

if grep -oP "validComponents\s*=\s*\[([^\]]+)\]" "$agent" > /dev/null 2>&1; then
    valid_line=$(grep -oP "validComponents\s*=\s*\[([^\]]+)\]" "$agent")
    for comp in "${COMPONENTS[@]}"; do
        if echo "$valid_line" | grep -q "'${comp}'" 2>/dev/null; then
            pass "jbRestore accepts: ${comp}"
        else
            fail "jbRestore does NOT accept: ${comp}"
        fi
    done
else
    fail "Could not find validComponents in jbRestore"
fi

# ─────────────────────────────────────────────────────────────
section "11. CLI --only Filter Supports Every Component"
# _collector_enabled must work for each component name
# ─────────────────────────────────────────────────────────────

# Check dry-run output lists each component
for comp in "${COMPONENTS[@]}"; do
    if grep -qP "(contents|has_home).*${comp}|_collector_enabled ${comp}|_should_run ${comp}" "$cli" 2>/dev/null; then
        pass "CLI --only supports: ${comp}"
    else
        fail "CLI --only might not support: ${comp}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "12. Backup Create Form Has Toggles for Key Components"
# The Create Backup form should let users select components
# ─────────────────────────────────────────────────────────────

# Check the jbRun component map in agent
component_map_keys=(files mysql postgres email dns ssl)
for comp in "${component_map_keys[@]}"; do
    if grep -qP "include_.*=>.*'${comp}'" "$agent" 2>/dev/null; then
        pass "backup form maps to: ${comp}"
    else
        fail "backup form MISSING mapping for: ${comp}"
    fi
done

# ─────────────────────────────────────────────────────────────
section "13. Restore Summary Shows Every Component"
# Step 2 review must show status for each selected component
# ─────────────────────────────────────────────────────────────

summary_components=(metadata files databases mysql_users postgres email dns ssl nginx php cron stalwart)
for key in "${summary_components[@]}"; do
    if grep -qP "restore_${key}|'${key}'" "$wizard" 2>/dev/null; then
        pass "review summary handles: ${key}"
    else
        fail "review summary MISSING: ${key}"
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

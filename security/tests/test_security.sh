#!/bin/bash
# ============================================================================
# Jabali Security — External Security Test Script
# ============================================================================
# Non-destructive tests against a live Jabali-protected site to verify
# that detection, WAF, WebShield, and brute-force protection are working.
#
# Usage:
#   ./tests/test_security.sh <target>
#   ./tests/test_security.sh jabali.site
#   ./tests/test_security.sh jabali.site --quick    # skip nmap (faster)
#
# Requirements: curl, nmap (optional), openssl
# ============================================================================
set -euo pipefail

# ── Config ──────────────────────────────────────────────────────────────────

TARGET="${1:-}"
QUICK="${2:-}"
PROTO="http"
REPORT_FILE="/tmp/jabali-security-test-$(date +%Y%m%d-%H%M%S).txt"

PASS=0
FAIL=0
WARN=0

# Legitimate browser UA to bypass WebShield bot challenge for actual vuln tests
BROWSER_UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

# ── Helpers ─────────────────────────────────────────────────────────────────

red()    { printf "\033[0;31m%s\033[0m\n" "$*"; }
green()  { printf "\033[0;32m%s\033[0m\n" "$*"; }
yellow() { printf "\033[0;33m%s\033[0m\n" "$*"; }
bold()   { printf "\033[1m%s\033[0m\n" "$*"; }
dim()    { printf "\033[2m%s\033[0m\n" "$*"; }

log() {
    local msg="$*"
    echo "$msg" | tee -a "$REPORT_FILE"
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

info() {
    dim "  [INFO] $*" | tee -a "$REPORT_FILE"
}

separator() {
    log ""
    log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

http_code() {
    curl -sk -o /dev/null -w "%{http_code}" --max-time 10 -H "User-Agent: ${BROWSER_UA}" "$@" 2>/dev/null || echo "000"
}

http_response() {
    curl -sk --max-time 10 -H "User-Agent: ${BROWSER_UA}" "$@" 2>/dev/null
}

http_headers() {
    curl -skI --max-time 10 -H "User-Agent: ${BROWSER_UA}" "$@" 2>/dev/null
}

# Raw curl without browser UA — for bot-specific tests
http_code_raw() {
    curl -sk -o /dev/null -w "%{http_code}" --max-time 10 "$@" 2>/dev/null || echo "000"
}

http_response_raw() {
    curl -sk --max-time 10 "$@" 2>/dev/null
}

# ── Validation ──────────────────────────────────────────────────────────────

if [ -z "$TARGET" ]; then
    echo "Usage: $0 <target-domain> [--quick]"
    echo "Example: $0 jabali.site"
    exit 1
fi

# Verify target is reachable
if ! curl -sk --max-time 5 -o /dev/null "${PROTO}://${TARGET}/" 2>/dev/null; then
    red "Error: Cannot reach ${PROTO}://${TARGET}/"
    red "Check the domain and try again."
    exit 1
fi

# ── Start ───────────────────────────────────────────────────────────────────

: > "$REPORT_FILE"

bold "╔════════════════════════════════════════════════════════════════════════╗"
bold "║           Jabali Security — External Test Suite                       ║"
bold "╚════════════════════════════════════════════════════════════════════════╝"
log ""
log "Target:  ${TARGET}"
log "Date:    $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
log "Report:  ${REPORT_FILE}"

# ============================================================================
# PHASE 1: RECONNAISSANCE
# ============================================================================
separator
bold "PHASE 1: RECONNAISSANCE"
separator

# -- 1.1 Port Scan --
if [ "$QUICK" != "--quick" ] && command -v nmap &>/dev/null; then
    log ""
    log "1.1 Port Scan (top 100 ports)"
    nmap_out=$(timeout 60 nmap -sV -T4 --top-ports 100 --host-timeout 30s "$TARGET" 2>&1)
    open_ports=$(echo "$nmap_out" | grep "^[0-9].*open" || true)
    if [ -n "$open_ports" ]; then
        while IFS= read -r line; do
            info "$line"
        done <<< "$open_ports"
    fi

    # Check for risky exposed ports
    if echo "$open_ports" | grep -qE "^3306|^5432|^6379|^27017|^11211"; then
        fail "Database/cache ports exposed to the internet"
    else
        pass "No database/cache ports exposed"
    fi

else
    log ""
    log "1.1 Port Scan — SKIPPED (--quick mode or nmap not installed)"
fi

# -- 1.2 HTTP Headers --
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

check_header "Strict-Transport-Security" "HSTS"
check_header "X-Frame-Options" "X-Frame-Options"
check_header "X-Content-Type-Options" "X-Content-Type-Options"
check_header "Content-Security-Policy" "Content-Security-Policy"
check_header "Referrer-Policy" "Referrer-Policy"
check_header "Permissions-Policy" "Permissions-Policy"
check_header "X-XSS-Protection" "X-XSS-Protection (legacy)"

# Server version disclosure
if echo "$headers" | grep -qi "^server:.*nginx/[0-9]"; then
    warn "Server header leaks nginx version"
else
    pass "Server header does not leak version"
fi

# -- 1.3 TLS Check --
log ""
log "1.3 TLS Certificate"
tls_info=$(echo | openssl s_client -connect "${TARGET}:443" -servername "$TARGET" 2>/dev/null | openssl x509 -noout -subject -issuer -dates 2>/dev/null || echo "FAIL")
if echo "$tls_info" | grep -q "subject="; then
    pass "Valid TLS certificate"
    info "$(echo "$tls_info" | grep 'subject=')"
    info "$(echo "$tls_info" | grep 'issuer=')"
    info "$(echo "$tls_info" | grep 'notAfter=')"
else
    fail "Could not verify TLS certificate"
fi

# -- 1.4 HTTP to HTTPS redirect --
log ""
log "1.4 HTTP -> HTTPS Redirect"
http_status=$(http_code "http://${TARGET}/")
if [ "$http_status" = "301" ] || [ "$http_status" = "302" ]; then
    pass "HTTP redirects to HTTPS (${http_status})"
else
    fail "HTTP does not redirect to HTTPS (got ${http_status})"
fi

# ============================================================================
# PHASE 2: WEBSHIELD TESTING
# ============================================================================
separator
bold "PHASE 2: WEBSHIELD TESTING (Bot Filtering + Rate Limiting)"
separator

# -- 2.1 Malicious User-Agents --
log ""
log "2.1 Malicious User-Agent Filtering"

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
        pass "Blocked user-agent '${name}' -> ${code}"
    elif [ "$code" = "200" ] || [ "$code" = "301" ] || [ "$code" = "302" ]; then
        warn "User-agent '${name}' NOT blocked -> ${code}"
    else
        info "User-agent '${name}' -> ${code}"
    fi
}

test_ua "sqlmap" "sqlmap/1.5#stable (http://sqlmap.org)"
test_ua "nikto" "Mozilla/5.00 (Nikto/2.1.6)"
test_ua "masscan" "masscan/1.3 (https://github.com/robertdavidgraham/masscan)"
test_ua "dirbuster" "DirBuster-1.0-RC1 (http://www.owasp.org/)"
test_ua "gobuster" "gobuster/3.1"
test_ua "wpscan" "WPScan v3.8.22"
test_ua "python-requests" "python-requests/2.28.1"
test_ua "curl-default" ""

# -- 2.2 Rate Limiting --
log ""
log "2.2 Rate Limiting (50 rapid requests with browser UA)"
rate_limited=false
for i in $(seq 1 50); do
    code=$(http_code "${PROTO}://${TARGET}/")
    if [ "$code" = "429" ]; then
        pass "Rate limited after ${i} requests -> 429"
        rate_limited=true
        break
    fi
done
if [ "$rate_limited" = false ]; then
    warn "No rate limiting detected after 50 rapid requests"
fi

# -- 2.3 JS Challenge --
log ""
log "2.3 JavaScript Challenge Detection"
body=$(http_response_raw -H "User-Agent: python-requests/2.28.1" "${PROTO}://${TARGET}/")
if echo "$body" | grep -qi "challenge\|captcha\|verify.*human\|jabali"; then
    pass "JS challenge page served for suspicious UA"
else
    code=$(http_code_raw -H "User-Agent: python-requests/2.28.1" "${PROTO}://${TARGET}/")
    if [ "$code" = "503" ]; then
        pass "Suspicious UA gets 503 challenge response"
    else
        warn "No JS challenge detected for suspicious UA (got ${code})"
    fi
fi

# ============================================================================
# PHASE 3: WEB VULNERABILITY PROBES
# ============================================================================
separator
bold "PHASE 3: WEB VULNERABILITY PROBES (WAF / Heuristic Detection)"
separator

# -- 3.1 Path Traversal --
log ""
log "3.1 Path Traversal Attempts"

test_traversal() {
    local payload="$1"
    local code body
    code=$(http_code "${PROTO}://${TARGET}${payload}")
    body=$(http_response "${PROTO}://${TARGET}${payload}")
    if echo "$body" | grep -q "root:"; then
        fail "Path traversal SUCCEEDED: ${payload}"
    elif [ "$code" = "403" ] || [ "$code" = "400" ] || [ "$code" = "444" ]; then
        pass "Path traversal blocked: ${payload} -> ${code}"
    else
        pass "Path traversal returned ${code} (no sensitive data)"
    fi
}

test_traversal "/../../../etc/passwd"
test_traversal "/wp-content/../../../etc/shadow"
test_traversal "/..%2f..%2f..%2fetc%2fpasswd"
test_traversal "/?file=../../../etc/passwd"
test_traversal "/?page=....//....//....//etc/passwd"

# -- 3.2 XSS Probes --
log ""
log "3.2 XSS Injection Probes"

test_xss() {
    local payload="$1"
    local code body
    code=$(http_code "${PROTO}://${TARGET}${payload}")
    body=$(http_response "${PROTO}://${TARGET}${payload}")
    if [ "$code" = "403" ] || [ "$code" = "400" ] || [ "$code" = "444" ]; then
        pass "XSS blocked -> ${code}"
    elif echo "$body" | grep -qi "<script>alert"; then
        fail "XSS reflected in response"
    else
        pass "XSS not reflected (${code})"
    fi
}

test_xss "/?s=<script>alert(1)</script>"
test_xss "/?s=<img%20src=x%20onerror=alert(1)>"
test_xss '/?s="><svg/onload=alert(1)>'
test_xss "/?s=javascript:alert(document.cookie)"
test_xss "/?s=<iframe%20src=%27javascript:alert(1)%27>"

# -- 3.3 SQL Injection Probes --
log ""
log "3.3 SQL Injection Probes"

test_sqli() {
    local payload="$1"
    local code
    code=$(http_code "${PROTO}://${TARGET}${payload}")
    if [ "$code" = "403" ] || [ "$code" = "400" ] || [ "$code" = "444" ]; then
        pass "SQLi blocked -> ${code}"
    elif [ "$code" = "500" ]; then
        warn "SQLi returned 500 (possible unhandled error)"
    else
        info "SQLi returned ${code}"
    fi
}

test_sqli "/?id=1%27%20OR%20%271%27=%271"
test_sqli "/?id=1%20UNION%20SELECT%20NULL,NULL,NULL--"
test_sqli "/?id=1;%20DROP%20TABLE%20users--"
test_sqli "/?s=1%27%20AND%201=1%20--%20-"
test_sqli "/wp-login.php?log=admin%27%20OR%201=1--&pwd=test"

# -- 3.4 Command Injection Probes --
log ""
log "3.4 Command Injection Probes"

test_cmdi() {
    local payload="$1"
    local code
    code=$(http_code "${PROTO}://${TARGET}${payload}")
    if [ "$code" = "403" ] || [ "$code" = "400" ] || [ "$code" = "444" ]; then
        pass "Command injection blocked -> ${code}"
    else
        info "Command injection returned ${code}"
    fi
}

test_cmdi "/?cmd=;cat%20/etc/passwd"
test_cmdi "/?file=|ls%20-la"
test_cmdi "/?ping=127.0.0.1%0als"
test_cmdi "/?input=%60whoami%60"

# -- 3.5 Webshell Pattern Probes --
log ""
log "3.5 Webshell Pattern Probes (GET-based)"

test_shell() {
    local payload="$1"
    local code
    code=$(http_code "${PROTO}://${TARGET}${payload}")
    if [ "$code" = "403" ] || [ "$code" = "400" ] || [ "$code" = "444" ]; then
        pass "Webshell pattern blocked -> ${code}"
    else
        info "Webshell pattern returned ${code}"
    fi
}

test_shell "/?cmd=system(%27id%27)"
test_shell "/?c=passthru(%27cat+/etc/passwd%27)"
test_shell "/?eval=base64_decode(%27cGhwaW5mbygp%27)"

# ============================================================================
# PHASE 4: WORDPRESS-SPECIFIC TESTS
# ============================================================================
separator
bold "PHASE 4: WORDPRESS-SPECIFIC TESTS"
separator

# -- 4.1 User Enumeration --
log ""
log "4.1 WordPress User Enumeration"

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

# WP REST API user enum
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

# -- 4.2 XML-RPC --
log ""
log "4.2 XML-RPC Endpoint"

xmlrpc_code=$(http_code "${PROTO}://${TARGET}/xmlrpc.php")
if [ "$xmlrpc_code" = "405" ] || [ "$xmlrpc_code" = "200" ]; then
    xmlrpc_body=$(curl -sk --max-time 10 -X POST \
        -H "Content-Type: text/xml" \
        -H "User-Agent: ${BROWSER_UA}" \
        -d '<?xml version="1.0"?><methodCall><methodName>system.listMethods</methodName></methodCall>' \
        "${PROTO}://${TARGET}/xmlrpc.php" 2>/dev/null)
    if echo "$xmlrpc_body" | grep -q "wp.getUsersBlogs"; then
        fail "XML-RPC is enabled and exposes methods (brute-force vector)"
    else
        warn "XML-RPC endpoint is accessible (${xmlrpc_code}) but methods may be limited"
    fi
elif [ "$xmlrpc_code" = "403" ] || [ "$xmlrpc_code" = "444" ]; then
    pass "XML-RPC endpoint blocked -> ${xmlrpc_code}"
else
    info "XML-RPC -> ${xmlrpc_code}"
fi

# -- 4.3 wp-login Brute Force (3 attempts) --
log ""
log "4.3 wp-login Brute-Force Detection (3 rapid failed logins)"

bf_blocked=false
for i in 1 2 3; do
    code=$(curl -sk -o /dev/null -w "%{http_code}" \
        -H "User-Agent: ${BROWSER_UA}" \
        -X POST "${PROTO}://${TARGET}/wp-login.php" \
        -d "log=admin&pwd=wrongpassword${i}&wp-submit=Log+In" \
        --max-time 10 2>/dev/null || echo "000")
    if [ "$code" = "403" ] || [ "$code" = "429" ] || [ "$code" = "444" ]; then
        pass "Brute-force blocked after attempt ${i} -> ${code}"
        bf_blocked=true
        break
    fi
done
if [ "$bf_blocked" = false ]; then
    warn "3 failed logins were not blocked (threshold may be higher)"
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
                fail "Sensitive file EXPOSED: ${path}"
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
        */readme.html|*/license.txt)
            if [ "$code" = "200" ]; then
                warn "Info file accessible (version disclosure): ${path}"
            else
                pass "Info file not accessible: ${path} -> ${code}"
            fi
            ;;
        *)
            if [ "$code" = "200" ]; then
                warn "Path accessible: ${path}"
            else
                info "${path} -> ${code}"
            fi
            ;;
    esac
}

test_sensitive "/wp-config.php"
test_sensitive "/wp-config.php.bak"
test_sensitive "/wp-config.php~"
test_sensitive "/.env"
test_sensitive "/.git/HEAD"
test_sensitive "/.git/config"
test_sensitive "/readme.html"
test_sensitive "/license.txt"
test_sensitive "/wp-admin/install.php"
test_sensitive "/wp-content/debug.log"
test_sensitive "/phpinfo.php"
test_sensitive "/info.php"
test_sensitive "/server-status"
test_sensitive "/server-info"

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

# ============================================================================
# PHASE 4B: PROACTIVE DEFENSE (requires SSH access)
# ============================================================================
separator
bold "PHASE 4B: PROACTIVE DEFENSE"
separator

# Detect SSH alias — try common names for the target host
SSH_HOST=""
for candidate in testserver "$TARGET"; do
    if ssh -o ConnectTimeout=5 -o BatchMode=yes "$candidate" "echo ok" &>/dev/null; then
        SSH_HOST="$candidate"
        break
    fi
done

if [ -n "$SSH_HOST" ]; then
    # -- 4b.1 Process Killer --
    log ""
    log "4b.3 Process Killer"

    kill_test=$(ssh -o ConnectTimeout=10 "$SSH_HOST" '
        # Find a non-root user with UID >= 1000
        test_user=$(awk -F: "\$3 >= 1000 && \$3 < 65534 {print \$1; exit}" /etc/passwd)
        if [ -z "$test_user" ]; then
            echo "no_user"
            exit 0
        fi

        # Spawn a process matching "Perl reverse shell" pattern (score=80)
        sudo -u "$test_user" perl -e "use IO::Socket; sleep(30)" &>/dev/null &
        child_pid=$!
        sleep 6

        # Check if it was killed
        if kill -0 "$child_pid" 2>/dev/null; then
            kill "$child_pid" 2>/dev/null
            echo "not_killed"
        else
            # Verify it was killed by Jabali (not just exited)
            if grep -q "KILLED.*pid=$child_pid" /var/log/jabali-security/jabali-security.log 2>/dev/null; then
                echo "killed"
            elif grep -q "KILLED" /var/log/jabali-security/jabali-security.log 2>/dev/null; then
                # PID may differ due to sudo forking
                recent_kill=$(grep "KILLED.*Perl reverse shell" /var/log/jabali-security/jabali-security.log | tail -1)
                if [ -n "$recent_kill" ]; then
                    echo "killed"
                else
                    echo "not_killed"
                fi
            else
                echo "exited"
            fi
        fi
    ' 2>/dev/null)

    if [ "$kill_test" = "killed" ]; then
        pass "Process killer detected and killed reverse shell pattern (score=80)"
    elif [ "$kill_test" = "not_killed" ]; then
        fail "Process killer did NOT kill suspicious process"
    elif [ "$kill_test" = "no_user" ]; then
        info "No non-root user available for process kill test"
    else
        info "Process kill test inconclusive: ${kill_test}"
    fi

    # -- 4b.4 UFW Firewall --
    log ""
    log "4b.4 UFW Firewall Status"

    ufw_status=$(ssh -o ConnectTimeout=10 "$SSH_HOST" 'ufw status verbose 2>/dev/null || echo "not_installed"' 2>/dev/null)

    if echo "$ufw_status" | grep -q "not_installed"; then
        info "UFW is not installed"
    elif echo "$ufw_status" | grep -q "Status: active"; then
        pass "UFW firewall is active"

        # Check default incoming is deny
        if echo "$ufw_status" | grep -q "deny (incoming)"; then
            pass "UFW default incoming policy: deny"
        else
            warn "UFW default incoming policy is not deny"
        fi

        # Check SSH is allowed (don't want lockouts)
        ufw_rules=$(ssh -o ConnectTimeout=10 "$SSH_HOST" 'ufw status numbered 2>/dev/null' 2>/dev/null)
        if echo "$ufw_rules" | grep -qE "22(/tcp)?\s+ALLOW"; then
            pass "UFW allows SSH (port 22)"
        else
            warn "UFW does not have an explicit SSH allow rule"
        fi

        # Check no database ports are open
        if echo "$ufw_rules" | grep -qE "(3306|5432|6379|27017|11211).*(ALLOW)"; then
            fail "UFW allows database/cache port access"
        else
            pass "UFW does not allow database/cache ports"
        fi

        # Count total rules
        rule_count=$(echo "$ufw_rules" | grep -c '^\[' || echo 0)
        info "UFW has ${rule_count} rules"
    else
        warn "UFW is installed but inactive"
    fi

    # -- 4b.5 UFW API Management --
    log ""
    log "4b.5 UFW API Management"

    api_key=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
        'grep "^API_KEY" /etc/jabali-security/jabali-security.conf 2>/dev/null | sed "s/API_KEY=\"//;s/\"//"' 2>/dev/null)

    if [ -n "$api_key" ]; then
        # Test UFW status endpoint
        ufw_api=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
            "curl -s -H 'X-API-Key: ${api_key}' --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/firewall/ufw/status 2>/dev/null" 2>/dev/null)

        if echo "$ufw_api" | grep -q '"success": true'; then
            pass "UFW API status endpoint responds"

            if echo "$ufw_api" | grep -q '"available": true'; then
                pass "UFW available on system"
            else
                info "UFW binary not found on system"
            fi

            if echo "$ufw_api" | grep -q '"active": true'; then
                pass "UFW reports active via API"
            fi
        elif echo "$ufw_api" | grep -q '"UFW management not enabled"'; then
            info "UFW management module is disabled (UFW_ENABLED=no)"
        else
            warn "UFW API status endpoint did not respond as expected"
        fi

        # Test that UFW API rejects unauthenticated requests
        ufw_noauth=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
            "curl -s --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/firewall/ufw/status 2>/dev/null" 2>/dev/null)
        if echo "$ufw_noauth" | grep -qi "invalid.*api.*key\|unauthorized"; then
            pass "UFW API rejects unauthenticated requests"
        elif echo "$ufw_noauth" | grep -q '"success": true'; then
            fail "UFW API accessible WITHOUT authentication"
        else
            info "UFW API auth test inconclusive"
        fi

        # Test that UFW API validates input (try injection in port)
        inject_test=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
            "curl -s -X POST -H 'X-API-Key: ${api_key}' -H 'Content-Type: application/json' \
            -d '{\"action\":\"allow\",\"port\":\"; rm -rf /\"}' \
            --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/firewall/ufw/rules 2>/dev/null" 2>/dev/null)
        if echo "$inject_test" | grep -q '"success": false'; then
            pass "UFW API rejects command injection in port field"
        elif echo "$inject_test" | grep -q '"success": true'; then
            fail "UFW API accepted malicious port value!"
        else
            info "UFW API injection test inconclusive"
        fi

        # Test adding and deleting a rule via API
        add_result=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
            "curl -s -X POST -H 'X-API-Key: ${api_key}' -H 'Content-Type: application/json' \
            -d '{\"action\":\"deny\",\"port\":\"19999\",\"protocol\":\"tcp\",\"comment\":\"jabali-test\"}' \
            --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/firewall/ufw/rules 2>/dev/null" 2>/dev/null)
        if echo "$add_result" | grep -q '"added": true'; then
            pass "UFW API: added test rule (deny 19999/tcp)"

            # Find and delete the test rule
            rules_json=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
                "curl -s -H 'X-API-Key: ${api_key}' --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/firewall/ufw/rules 2>/dev/null" 2>/dev/null)
            test_rule_num=$(echo "$rules_json" | grep -oP '"number":\s*\K\d+(?=.*19999)' | tail -1)
            if [ -n "$test_rule_num" ]; then
                del_result=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
                    "curl -s -X DELETE -H 'X-API-Key: ${api_key}' \
                    --unix-socket /run/jabali-security/jabali-security.sock http://localhost/api/v1/firewall/ufw/rules/${test_rule_num} 2>/dev/null" 2>/dev/null)
                if echo "$del_result" | grep -q '"deleted": true'; then
                    pass "UFW API: deleted test rule #${test_rule_num}"
                else
                    warn "UFW API: could not delete test rule #${test_rule_num}"
                fi
            else
                warn "Could not find test rule number to clean up"
            fi
        else
            warn "UFW API: could not add test rule"
        fi
    else
        info "Could not read API key for UFW API tests"
    fi

else
    log ""
    log "4b. Proactive Defense — SKIPPED (no SSH access to target)"
    info "To test, ensure SSH access to the target via key-based auth"
fi

# ============================================================================
# PHASE 5: JABALI API (UNIX SOCKET)
# ============================================================================
separator
bold "PHASE 5: JABALI REST API (UNIX SOCKET)"
separator

log ""
log "5.1 API Socket Accessibility"

# Verify Unix socket exists on the server
if [ -n "${SSH_HOST:-}" ]; then
    sock_exists=$(ssh -o ConnectTimeout=10 "$SSH_HOST" \
        'test -S /run/jabali-security/jabali-security.sock && echo yes || echo no' 2>/dev/null)
    if [ "$sock_exists" = "yes" ]; then
        pass "API Unix socket exists at /run/jabali-security/jabali-security.sock"
    else
        warn "API Unix socket not found (daemon may not be running)"
    fi
fi

log ""
log "5.2 TCP Port 9876 Not Exposed"

api_code=$(http_code "http://${TARGET}:9876/api/v1/status" 2>/dev/null)
if [ "$api_code" = "000" ]; then
    pass "TCP port 9876 not reachable externally (Unix socket only — correct)"
else
    fail "TCP port 9876 is reachable externally (should be disabled) -> ${api_code}"
fi

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
green "  PASS: ${PASS}"
if [ "$FAIL" -gt 0 ]; then
    red "  FAIL: ${FAIL}"
else
    log "  FAIL: ${FAIL}"
fi
if [ "$WARN" -gt 0 ]; then
    yellow "  WARN: ${WARN}"
else
    log "  WARN: ${WARN}"
fi
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

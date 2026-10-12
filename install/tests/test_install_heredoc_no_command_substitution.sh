#!/usr/bin/env bash
# install/tests/test_install_heredoc_no_command_substitution.sh — GH #2056, #2101.
#
# In an unquoted heredoc (<<WORD), bash runs `...` and $(...) even inside lines
# that look like comments of the file being written. The PowerDNS drop-in
# template (cat > "$pdns_conf_new" <<PDNSCONF) had a comment ending "repairs the
# host on the next `jabali update` …", so turning DNS on from Server Settings →
# Modules ran a nested `jabali update` (git fetch + reset of the repo, in the
# middle of the install) and pasted its progress lines into
# /etc/powerdns/pdns.d/01-jabali-mysql.conf. PowerDNS then refused to start:
#   Fatal error: Trying to set unknown setting '→ ensure mail ACME webroot …'
# Existing tests read the template as text, so they never ran it.
#
#   1. No unquoted heredoc in install.sh has an unescaped backtick.
#   2. The PowerDNS template, rendered by bash with a fake `jabali` (and fake
#      `pending-`) on PATH, runs none of them and holds only comments, blank
#      lines and key=value settings.
#   3. ensure_pdns_conf_no_stray_lines heals a host that already has the
#      stray lines: it comments them out, keeps every setting, restarts pdns
#      only when it is enabled, no-ops on a clean file, and skips a host
#      without the file. `jabali update` (provision_new_software) calls it.
#
# Run from repo root:
#     bash install/tests/test_install_heredoc_no_command_substitution.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# --- 1. No unescaped backtick inside an unquoted heredoc. ---
hits=$(python3 - install.sh <<'PY'
import re, sys
lines = open(sys.argv[1], encoding="utf-8", errors="replace").read().split("\n")
out = []
i = 0
while i < len(lines):
    l = lines[i]
    m = None
    if not l.lstrip().startswith("#"):
        for mm in re.finditer(r"<<(-?)\s*(['\"\\]?)([A-Za-z_][A-Za-z0-9_]*)", l):
            if mm.group(2) == "":
                m = mm
    if m:
        word, dash = m.group(3), m.group(1) == "-"
        j = i + 1
        while j < len(lines):
            body = lines[j].lstrip("\t") if dash else lines[j]
            if body == word:
                break
            for k, c in enumerate(lines[j]):
                if c != "`":
                    continue
                bs, p = 0, k - 1
                while p >= 0 and lines[j][p] == "\\":
                    bs, p = bs + 1, p - 1
                if bs % 2 == 0:
                    out.append(f"install.sh:{j + 1}: <<{word}: {lines[j].strip()[:100]}")
                    break
            j += 1
        i = j
    i += 1
print("\n".join(out))
PY
)
if [[ -n "$hits" ]]; then
  echo "FAIL: unescaped backtick in an unquoted heredoc — bash runs it while writing the file (write \\\` instead):"
  echo "$hits"
  fail=1
fi

# --- 2. Render the PowerDNS template for real. ---
template=$(awk '/cat > "\$pdns_conf_new" <<PDNSCONF/,/^PDNSCONF$/' install.sh)
if [[ -z "$template" ]]; then
  echo "FAIL: PowerDNS template (cat > \"\$pdns_conf_new\" <<PDNSCONF) not found in install.sh"
  exit 1
fi
mkdir -p "$tmp/bin"
for cmd in jabali pending-; do
  printf '#!/bin/sh\necho "RAN-%s $*"\necho "→ a second line of output"\n' "$cmd" >"$tmp/bin/$cmd"
  chmod +x "$tmp/bin/$cmd"
done
pdns_conf_new="$tmp/01-jabali-mysql.conf"
pdns_password=test-password
pdns_local_addresses=127.0.0.1
( cd "$tmp" && PATH="$tmp/bin:$PATH" eval "$template" ) 2>"$tmp/render.err" || true
if grep -q 'RAN-' "$pdns_conf_new" 2>/dev/null; then
  echo "FAIL: rendering the PowerDNS template ran a command and wrote its output into the config:"
  grep -n 'RAN-' "$pdns_conf_new" | head -5
  fail=1
fi
if [[ -e "$tmp/d_replacePending" ]]; then
  echo "FAIL: rendering the PowerDNS template created a file named d_replacePending (a \`pending->d_replacePending\` comment ran)"
  fail=1
fi
stray=$(grep -vnE '^[[:space:]]*(#|$)|^[a-z0-9-]+=' "$pdns_conf_new" || true)
if [[ -n "$stray" ]]; then
  echo "FAIL: the rendered PowerDNS config has lines that are not comments or settings (pdns refuses to start):"
  echo "$stray" | head -5
  fail=1
fi
grep -q '^gmysql-password=test-password$' "$pdns_conf_new" \
  || { echo "FAIL: the template no longer expands \${pdns_password}"; fail=1; }

# --- 3. The update-path heal. ---
fn_src=$(awk '/^ensure_pdns_conf_no_stray_lines\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_pdns_conf_no_stray_lines not defined in install.sh"
  echo "RESULT: FAIL"
  exit 1
fi
pdns_enabled=0
systemctl() {
  echo "systemctl $*" >>"$tmp/systemctl.log"
  case "$1" in
    is-enabled) [[ "$pdns_enabled" == 1 ]] ;;
    *) return 0 ;;
  esac
}
_ok() { :; }
_log() { :; }
_warn() { :; }
eval "${fn_src//\/etc\/powerdns\/pdns.d\/01-jabali-mysql.conf/$tmp/heal.conf}"

broken='launch=gmysql
gmysql-password=secret
# after the main pdns.conf, so this line overrides such a permissive value and
# repairs the host on the next → fix repo ownership
→ ensure mail ACME webroot (/var/www/jabali-acme)
→ git fetch + reset to release channel
7715b0dc50579423d79036cafa5466c6f33f41c3 (config change → pdns restart).

allow-axfr-ips=127.0.0.0/8,::1
zone-cache-refresh-interval=0'

printf '%s\n' "$broken" >"$tmp/heal.conf"
: >"$tmp/systemctl.log"
pdns_enabled=1
ensure_pdns_conf_no_stray_lines
left=$(grep -vnE '^[[:space:]]*(#|$)|^[a-z0-9-]+=' "$tmp/heal.conf" || true)
[[ -z "$left" ]] || { echo "FAIL: heal left stray lines: $left"; fail=1; }
for s in launch=gmysql gmysql-password=secret 'allow-axfr-ips=127.0.0.0/8,::1' zone-cache-refresh-interval=0; do
  grep -qxF "$s" "$tmp/heal.conf" || { echo "FAIL: heal lost the setting $s"; fail=1; }
done
grep -q 'restart pdns' "$tmp/systemctl.log" || { echo "FAIL: heal didn't restart an enabled pdns"; fail=1; }
grep -q 'reset-failed pdns' "$tmp/systemctl.log" || { echo "FAIL: heal didn't reset-failed pdns (a crash loop hits the start limit)"; fail=1; }

printf '%s\n' "$broken" >"$tmp/heal.conf"
: >"$tmp/systemctl.log"
pdns_enabled=0
ensure_pdns_conf_no_stray_lines
grep -q 'restart pdns' "$tmp/systemctl.log" && { echo "FAIL: heal restarted pdns while it is disabled/masked"; fail=1; }

cp "$tmp/heal.conf" "$tmp/clean.before"
: >"$tmp/systemctl.log"
pdns_enabled=1
ensure_pdns_conf_no_stray_lines
cmp -s "$tmp/clean.before" "$tmp/heal.conf" || { echo "FAIL: heal changed an already clean file"; fail=1; }
grep -q 'restart' "$tmp/systemctl.log" && { echo "FAIL: heal restarted pdns for a clean file"; fail=1; }

rm -f "$tmp/heal.conf"
ensure_pdns_conf_no_stray_lines || { echo "FAIL: heal failed on a host without the file"; fail=1; }

awk '/^provision_new_software\(\)/{p=1} p&&/ensure_pdns_conf_no_stray_lines/{print} p&&/^}/{exit}' install.sh | grep -q . \
  || { echo "FAIL: provision_new_software (jabali update) doesn't call ensure_pdns_conf_no_stray_lines — broken hosts never heal"; fail=1; }

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: no command substitution in install.sh heredocs; the PowerDNS template renders clean; update heals stray lines (GH #2056, #2101)"

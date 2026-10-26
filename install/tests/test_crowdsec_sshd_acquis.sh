#!/usr/bin/env bash
# install/tests/test_crowdsec_sshd_acquis.sh — the CrowdSec sshd acquisition
# counts only lines the SSH server itself writes.
#
# Any process that can reach the syslog socket can set SYSLOG_IDENTIFIER, so
# the journalctl filter must also require _UID=0 (journald takes it from the
# kernel; OpenSSH logs every auth event from its root monitor). This pins:
#   1. install_crowdsec_appsec's desired file and the provision_new_software
#      converger's heredoc are the same bytes (two copies must not drift).
#   2. That content filters on _UID=0 and both identifiers (sshd and
#      sshd-session, Debian 13 split mode).
#   3. The converger rewrites a file that lacks _UID=0 and leaves the
#      current file alone.
#
# Run from repo root:  bash install/tests/test_crowdsec_sshd_acquis.sh
# INSTALL_SH=<path> tests another copy of install.sh. Exit 0 = pass.
set -euo pipefail
cd "$(dirname "$0")/../.."
INSTALL_SH="${INSTALL_SH:-install.sh}"

fail=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- 1. both copies, byte for byte. ---
desired_line="$(grep -m1 '^  local desired_sshd_acquis=' "$INSTALL_SH" || true)"
if [[ -z "$desired_line" ]]; then
  echo "FAIL: desired_sshd_acquis not found in $INSTALL_SH"
  exit 1
fi
# The value is a $'...' literal; evaluating the assignment yields the exact
# bytes install_crowdsec_appsec writes.
eval "${desired_line#  local }"
printf '%s' "$desired_sshd_acquis" >"$tmp/desired.yaml"

awk '/cat >"\$_tmp_acquis" <<'"'"'EOF'"'"'/{p=1; next} p&&/^EOF$/{exit} p' "$INSTALL_SH" >"$tmp/converger.yaml"
if [[ ! -s "$tmp/converger.yaml" ]]; then
  echo "FAIL: converger heredoc for jabali-sshd.yaml not found in $INSTALL_SH"
  fail=1
elif ! cmp -s "$tmp/desired.yaml" "$tmp/converger.yaml"; then
  echo "FAIL: install_crowdsec_appsec and the converger write different jabali-sshd.yaml:"
  diff "$tmp/desired.yaml" "$tmp/converger.yaml" || true
  fail=1
fi

# --- 2. the filter. ---
for want in '  - "_UID=0"' '  - "SYSLOG_IDENTIFIER=sshd"' '  - "SYSLOG_IDENTIFIER=sshd-session"'; do
  if ! grep -qxF -- "$want" "$tmp/desired.yaml"; then
    echo "FAIL: jabali-sshd.yaml filter lacks: $want"
    fail=1
  fi
done

# --- 3. the converger's trigger, evaluated as written. ---
cond="$(awk '/if \[\[ -f "\$_sshd_acquis" \]\] && \\$/{p=1} p{print} p&&/; then$/{exit}' "$INSTALL_SH")"
if [[ -z "$cond" ]]; then
  echo "FAIL: converger trigger for jabali-sshd.yaml not found in $INSTALL_SH"
  fail=1
else
  cond="${cond#*if }"
  cond="${cond%; then}"
  triggers() {
    local _sshd_acquis="$1"
    eval "if $cond; then return 0; else return 1; fi"
  }
  # The filter every host had before _UID=0.
  printf '%s\n' 'source: journalctl' 'journalctl_filter:' \
    '  - "SYSLOG_IDENTIFIER=sshd"' '  - "SYSLOG_IDENTIFIER=sshd-session"' \
    'labels:' '  type: syslog' >"$tmp/old.yaml"
  if ! triggers "$tmp/old.yaml"; then
    echo "FAIL: the converger leaves a filter without _UID=0 in place"
    fail=1
  fi
  if triggers "$tmp/desired.yaml"; then
    echo "FAIL: the converger rewrites the current file on every update"
    fail=1
  fi
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi
echo "PASS: jabali-sshd.yaml counts only the SSH server's own lines (_UID=0), both copies match"

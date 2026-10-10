#!/usr/bin/env bash
# install/tests/test_mailrelay_unit.sh — the website mail relay (GH #2056,
# ADR 0174) holds the smarthost login, so its unit must keep it boxed in, and
# only the agent may switch it on.
#
# Asserts, source-level:
#   1. The unit runs `jabali-agent mailrelay` as the jabali-mailrelay user,
#      never root, with no capabilities, no privilege gain and a read-only
#      system.
#   2. install.sh never enables or starts it: mail.relay.apply does, and only
#      when the admin picks the smarthost.
#   3. provision_new_software (every update) calls ensure_jabali_mailrelay,
#      and main() (fresh install) does too.
#
# Run from repo root:
#     bash install/tests/test_mailrelay_unit.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

unit="install/systemd/jabali-mailrelay.service"
fail=0

need_line() {
  if ! grep -qxF "$1" "$unit"; then
    echo "FAIL: $unit lacks \`$1\` — $2"
    fail=1
  fi
}

need_line "User=jabali-mailrelay" "the relay must not run as root or as the agent"
need_line "Group=jabali-mailrelay" "the config is 0640 root:jabali-mailrelay"
need_line "ExecStart=/usr/local/bin/jabali-agent mailrelay" "the relay ships in the agent binary"
need_line "NoNewPrivileges=yes" "no privilege gain"
need_line "CapabilityBoundingSet=" "no capabilities"
need_line "ProtectSystem=strict" "read-only system"
need_line "ProtectHome=yes" "no access to the sites' files"
need_line "RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6" "only its socket and the smarthost"
need_line "PartOf=jabali-agent.service" "an agent restart must move a running relay onto the new binary"

if grep -Eq '^User=root$|^DynamicUser=' "$unit"; then
  echo "FAIL: $unit runs as root or a dynamic user"
  fail=1
fi

if grep -nE 'systemctl[^#]*(enable|start|restart|reload-or-restart)[^#]*jabali-mailrelay' install.sh \
    | grep -v 'try-restart'; then
  echo "FAIL: install.sh switches the relay on; only mail.relay.apply may (smarthost selected)"
  fail=1
fi

body=$(awk '/^provision_new_software\(\) *\{/ {inside = 1; next} inside && /^\}/ {exit} inside' install.sh)
if [[ -z "$body" ]]; then
  echo "FAIL: provision_new_software not found in install.sh"
  fail=1
elif ! grep -q 'ensure_jabali_mailrelay' <<<"$body"; then
  echo "FAIL: provision_new_software doesn't call ensure_jabali_mailrelay — updated boxes never get the relay user + unit"
  fail=1
fi

mainbody=$(awk '/^main\(\) *\{/ {inside = 1; next} inside && /^\}/ {exit} inside' install.sh)
if ! grep -q 'ensure_jabali_mailrelay' <<<"$mainbody"; then
  echo "FAIL: main() doesn't call ensure_jabali_mailrelay — fresh installs never get the relay user + unit"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "PASS: the website mail relay runs boxed in as its own user, and only the agent switches it on"

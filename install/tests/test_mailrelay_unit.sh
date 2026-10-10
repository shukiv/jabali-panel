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
#   4. The relay only runs as the dedicated local system account
#      (mailrelay_account_ok): it holds the smarthost password.
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

# 4. mailrelay_account_ok accepts only the dedicated local system account.
fn=$(sed -n '/^mailrelay_account_ok() *{/,/^}/p' install.sh)
if [[ -z "$fn" ]]; then
  echo "FAIL: mailrelay_account_ok not found in install.sh"
  fail=1
else
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  printf 'SYS_UID_MAX\t999\n' >"$tmp/login.defs"
  fn=${fn//\/etc\/passwd/$tmp\/passwd}
  fn=${fn//\/etc\/group/$tmp\/group}
  fn=${fn//\/etc\/login.defs/$tmp\/login.defs}
  eval "$fn"
  check() { # <want 0|1> <passwd line> <group line> <case>
    printf '%s\n' "root:x:0:0::/root:/bin/bash" "$2" >"$tmp/passwd"
    printf '%s\n' "root:x:0:" "$3" >"$tmp/group"
    local got=0
    mailrelay_account_ok || got=1
    if [[ "$got" != "$1" ]]; then
      echo "FAIL: mailrelay_account_ok for $4: got $got, want $1"
      fail=1
    fi
  }
  check 0 "jabali-mailrelay:x:990:990::/nonexistent:/usr/sbin/nologin" "jabali-mailrelay:x:990:" "the dedicated account"
  check 1 "jabali-mailrelay:x:0:990::/nonexistent:/usr/sbin/nologin" "jabali-mailrelay:x:990:" "uid 0"
  check 1 "jabali-mailrelay:x:1500:1500::/home/x:/usr/sbin/nologin" "jabali-mailrelay:x:1500:" "a login-range uid"
  check 1 "jabali-mailrelay:x:990:990::/nonexistent:/bin/bash" "jabali-mailrelay:x:990:" "a login shell"
  check 1 "jabali-mailrelay:x:990:33::/nonexistent:/usr/sbin/nologin" "jabali-mailrelay:x:990:" "another primary group"
  check 1 "jabali-mailrelay:x:990:990::/nonexistent:/usr/sbin/nologin" "jabali-mailrelay:x:990:alice" "a group with members"
  check 1 "otheruser:x:990:990::/nonexistent:/usr/sbin/nologin" "jabali-mailrelay:x:990:" "no local account (a directory service's)"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "PASS: the website mail relay runs boxed in as its own user, and only the agent switches it on"

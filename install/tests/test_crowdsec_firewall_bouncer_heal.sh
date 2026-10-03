#!/usr/bin/env bash
# install/tests/test_crowdsec_firewall_bouncer_heal.sh — the CrowdSec firewall
# bouncer must not stay silently down.
#
# A fleet box ran for weeks with crowdsec-firewall-bouncer-nftables unpacked
# but never configured (its postinst stopped at the conffile prompt for
# crowdsec-firewall-bouncer.yaml with no stdin), so the bouncer never started
# and banned IPs were not dropped at the firewall. Nothing noticed:
#   - install_crowdsec treated the unpacked package as installed (`dpkg -s`),
#     and looked for ${bouncer_pkg}.yaml / ${bouncer_pkg}.service, names the
#     packages never use, so its key setup and health check never ran;
#   - `jabali update` never checked the bouncer at all.
#
# 1. Functional: heal_crowdsec_firewall_bouncer (run by `jabali update`) with
#    dpkg-query, dpkg, systemctl and cscli stubbed.
# 2. Static: install_crowdsec uses the real names and package state, and the
#    update steps call the heal.
#
# Run from repo root:
#     bash install/tests/test_crowdsec_firewall_bouncer_heal.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

body=$(awk '/^heal_crowdsec_firewall_bouncer\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh)
if [[ -z "$body" ]]; then
  echo "FAIL: heal_crowdsec_firewall_bouncer() not found in install.sh"
  exit 1
fi

# run_case <cscli present 0/1> <nftables pkg status> <unit active 0/1>; prints recorded calls
run_case() {
  (
    HAVE_CSCLI=$1 PKG_STATUS=$2 ACTIVE=$3
    PATH=/nonexistent
    _log() { :; }
    _ok() { :; }
    _warn() { :; }
    sleep() { :; }
    if [[ "$HAVE_CSCLI" == 1 ]]; then cscli() { :; }; fi
    dpkg-query() {
      case "${*: -1}" in
        crowdsec-firewall-bouncer-nftables) printf '%s' "$PKG_STATUS" ;;
        *) printf 'unknown ok not-installed' ;;
      esac
    }
    dpkg() { echo "dpkg $*" >&3; }
    systemctl() {
      case "$1" in
        cat) return 0 ;;
        is-enabled) [[ "$ACTIVE" == 1 ]] ;;
        is-active) [[ "$ACTIVE" == 1 ]] ;;
        *) echo "systemctl $*" >&3 ;;
      esac
    }
    eval "$body"
    heal_crowdsec_firewall_bouncer >/dev/null 2>&1
  ) 3>&1
}

out=$(run_case 1 "install ok unpacked" 0)
if ! grep -q 'dpkg --configure --force-confold .*crowdsec-firewall-bouncer-nftables' <<<"$out"; then
  echo "FAIL: an unpacked bouncer package was not configured with --force-confold (calls: ${out:-none})"
  fail=1
fi
if ! grep -q 'systemctl start crowdsec-firewall-bouncer.service' <<<"$out"; then
  echo "FAIL: a bouncer that is down was not started (calls: ${out:-none})"
  fail=1
fi

out=$(run_case 1 "install ok half-configured" 1)
if ! grep -q 'dpkg --configure' <<<"$out"; then
  echo "FAIL: a half-configured bouncer package was not configured (calls: ${out:-none})"
  fail=1
fi

out=$(run_case 1 "install ok installed" 1)
if [[ -n "$out" ]]; then
  echo "FAIL: a configured, running bouncer must be left alone, got: $out"
  fail=1
fi

out=$(run_case 1 "deinstall ok config-files" 1)
if grep -q 'dpkg' <<<"$out"; then
  echo "FAIL: a removed package (config files only) must not be configured: $out"
  fail=1
fi

out=$(run_case 0 "install ok unpacked" 0)
if [[ -n "$out" ]]; then
  echo "FAIL: without CrowdSec (no cscli) the heal must do nothing, got: $out"
  fail=1
fi

# --- static ---
# Code lines only: comments may quote the old names.
crowdsec_fn=$(awk '/^install_crowdsec\(\)/{f=1} f{print} f&&/^\}/{exit}' install.sh | grep -vE '^[[:space:]]*#')
if [[ -z "$crowdsec_fn" ]]; then
  echo "FAIL: install_crowdsec() not found in install.sh"
  fail=1
else
  if grep -qE '\$\{bouncer_pkg\}\.(yaml|service)' <<<"$crowdsec_fn"; then
    echo "FAIL: install_crowdsec uses \${bouncer_pkg}.yaml/.service; the packages ship crowdsec-firewall-bouncer.yaml/.service"
    fail=1
  fi
  if ! grep -q '/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml' <<<"$crowdsec_fn"; then
    echo "FAIL: install_crowdsec does not manage /etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml"
    fail=1
  fi
  if grep -qE 'dpkg -s "\$bouncer_pkg"' <<<"$crowdsec_fn"; then
    echo "FAIL: install_crowdsec checks the bouncer with dpkg -s, which also succeeds for an unpacked package"
    fail=1
  fi
fi
if ! grep -q 'heal_crowdsec_firewall_bouncer' panel-api/cmd/server/update.go; then
  echo "FAIL: jabali update does not call heal_crowdsec_firewall_bouncer — existing boxes never heal"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: firewall bouncer: unpacked/half-configured package finished with confold, a down service started, update calls the heal, install_crowdsec uses the real names"

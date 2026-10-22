#!/usr/bin/env bash
# Security advisories reach every server within the hour.
#
# jabali-security-feed.timer runs `jabali security feed-check` hourly; for a
# new advisory it installs the pending security updates or starts the panel
# update now, and alerts the admins (panel-api/cmd/server/security_feed.go).
# ensure_security_feed_timer installs the units on fresh installs and on every
# `jabali update`. This checks the converger, the units, both call sites, the
# uninstall and the Cron Jobs catalog entry.
#
# Run from repo root:
#     bash install/tests/test_security_feed_timer.sh
set -uo pipefail

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn_src=$(awk '/^ensure_security_feed_timer\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_security_feed_timer not defined in install.sh"
  echo "RESULT: FAIL"
  exit 1
fi
units="$tmp/etc/systemd/system"
mkdir -p "$units"
fn_tmp="${fn_src//local unit_dir=\/etc\/systemd\/system/local unit_dir=$units}"
grep -qF "local unit_dir=$units" <<<"$fn_tmp" || { echo "FAIL: could not re-point the unit dir"; exit 1; }

export REPO_DIR="$tmp/repo"
mkdir -p "$REPO_DIR/install/systemd"
cp install/systemd/jabali-security-feed.service install/systemd/jabali-security-feed.timer "$REPO_DIR/install/systemd/"
calls="$tmp/calls"
run_converger() {
  : >"$calls"
  (
    _ok() { echo "ok $*" >>"$calls"; }
    _warn() { echo "warn $*" >>"$calls"; }
    install() { local a=("$@"); cp "${a[-2]}" "${a[-1]}"; }
    systemctl() { echo "systemctl $*" >>"$calls"; }
    eval "$fn_tmp"
    ensure_security_feed_timer
  )
}

run_converger
for u in jabali-security-feed.service jabali-security-feed.timer; do
  cmp -s "install/systemd/$u" "$units/$u" || { echo "FAIL: $u was not installed"; fail=1; }
done
grep -qx "systemctl daemon-reload" "$calls" || { echo "FAIL: no daemon-reload after installing the units"; fail=1; }
grep -qx "systemctl enable --now jabali-security-feed.timer" "$calls" || { echo "FAIL: the timer was not enabled"; fail=1; }

# Unchanged units: no daemon-reload, but the timer is still enabled.
run_converger
if grep -qx "systemctl daemon-reload" "$calls"; then echo "FAIL: daemon-reload on every run"; fail=1; fi
grep -qx "systemctl enable --now jabali-security-feed.timer" "$calls" || { echo "FAIL: second run did not enable the timer"; fail=1; }

# A unit missing from the checkout: warn, install nothing.
rm -f "$units"/jabali-security-feed.*
rm -f "$REPO_DIR/install/systemd/jabali-security-feed.timer"
run_converger
if compgen -G "$units/jabali-security-feed.*" >/dev/null; then echo "FAIL: installed a half pair"; fail=1; fi
grep -q "^warn jabali-security-feed.timer missing" "$calls" || { echo "FAIL: no warning for a missing unit"; fail=1; }
if grep -q "^systemctl" "$calls"; then echo "FAIL: touched systemd with a unit missing"; fail=1; fi

# The units.
svc=install/systemd/jabali-security-feed.service
tmr=install/systemd/jabali-security-feed.timer
grep -qx "ExecStart=/usr/local/bin/jabali security feed-check" "$svc" || { echo "FAIL: $svc does not run the feed check"; fail=1; }
grep -qx "Type=oneshot" "$svc" || { echo "FAIL: $svc is not oneshot"; fail=1; }
grep -qx "OnCalendar=hourly" "$tmr" || { echo "FAIL: $tmr is not hourly"; fail=1; }
grep -qE "^RandomizedDelaySec=[0-9]+min$" "$tmr" || { echo "FAIL: $tmr fires on every server at once"; fail=1; }
grep -qx "Unit=jabali-security-feed.service" "$tmr" || { echo "FAIL: $tmr does not start the service"; fail=1; }

# Both call sites, uninstall, and the Cron Jobs catalog.
pns=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
grep -qE '^  ensure_security_feed_timer$' <<<"$pns" || { echo "FAIL: jabali update does not install the timer"; fail=1; }
mainfn=$(awk '/^main\(\) \{$/,/^\}$/' install.sh)
grep -qE '^  ensure_security_feed_timer( |$)' <<<"$mainfn" || { echo "FAIL: a fresh install does not install the timer"; fail=1; }
for line in '  rm -f  /etc/systemd/system/jabali-security-feed.service' '  rm -f  /etc/systemd/system/jabali-security-feed.timer' \
            '    jabali-security-feed.timer \' '    jabali-security-feed.service \' \
            '         /var/lib/jabali-security-feed \'; do
  grep -qxF -- "$line" install.sh || { echo "FAIL: uninstall lacks: $line"; fail=1; }
done
grep -qF 'Timer: "jabali-security-feed.timer", Service: "jabali-security-feed.service"' internal/systemjobs/systemjobs.go \
  || { echo "FAIL: the timer is not in the Cron Jobs catalog"; fail=1; }

if ((fail == 0)); then
  echo "PASS: every server checks the security advisories hourly"
  exit 0
fi
echo "RESULT: FAIL"
exit 1

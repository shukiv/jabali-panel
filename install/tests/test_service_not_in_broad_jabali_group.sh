#!/usr/bin/env bash
# install/tests/test_service_not_in_broad_jabali_group.sh — JAB-357 criterion 2.
#
# The broad `jabali` group owns the root Agent socket (/run/jabali/agent.sock,
# 0660 root:jabali) and panel secrets under /etc/jabali-panel. No NON-PANEL
# service identity (a distinct low-privilege service user — jabali-mail /
# Stalwart, jabali-webmail / Bulwark) may hold that group: a compromise of an
# internet-facing service would otherwise reach host root at the FS layer even
# with the SO_PEERCRED gate (defense in depth).
#
# A group grant hides in TWO places, and a unit-granted group never shows up in
# /etc/group — so both must be asserted:
#   1. install.sh must not `usermod -a -G <jabali> <service>` those accounts.
#   2. no systemd unit whose User= is a non-panel service account may declare
#      `SupplementaryGroups=jabali`.
# Also asserts the upgrade converger exists + runs on `jabali update`.
#
# Panel-domain units (User=jabali or root — jabali-panel, jabali-kratos) are
# exempt: `jabali` is their own identity, not a broad grant.
#
# Run from repo root:
#     bash install/tests/test_service_not_in_broad_jabali_group.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0

# Non-panel service accounts that must never hold the broad jabali group.
NONPANEL_SERVICES=(jabali-mail jabali-webmail)

# 1. install.sh must not usermod any non-panel service into the panel group.
#    $SERVICE_USER is the panel account (jabali); the grant reads as
#    `usermod -a -G "$SERVICE_USER" <service>` or a literal `... -G jabali ...`.
for svc in "${NONPANEL_SERVICES[@]}"; do
  if grep -nE "usermod .*-G [\"']?(\\\$SERVICE_USER|jabali)[\"']? .*${svc}\\b" install.sh \
       | grep -vqE '^\s*#'; then
    echo "FAIL: install.sh adds ${svc} to the broad jabali group (usermod -a -G) — JAB-357"
    grep -nE "usermod .*-G .*${svc}\\b" install.sh || true
    fail=1
  fi
done

# 2. No non-panel unit may carry SupplementaryGroups=jabali (bare token).
for unit in install/systemd/*.service; do
  [[ -f "$unit" ]] || continue
  user_line=$(grep -E '^User=' "$unit" | head -1 | cut -d= -f2- || true)
  # Only inspect units that run as a non-panel service account.
  is_nonpanel=0
  for svc in "${NONPANEL_SERVICES[@]}"; do
    [[ "$user_line" == "$svc" ]] && is_nonpanel=1
  done
  [[ "$is_nonpanel" -eq 1 ]] || continue
  # SupplementaryGroups= is space-separated; flag the bare `jabali` token only
  # (NOT jabali-webmail / jabali-sockets — the hyphen makes grep -w match, so
  # anchor on whitespace/line boundaries instead).
  supp=$(grep -E '^SupplementaryGroups=' "$unit" | head -1 | cut -d= -f2- || true)
  if [[ -n "$supp" ]] && grep -qE '(^| )jabali( |$)' <<<"$supp"; then
    echo "FAIL: $unit (User=$user_line) declares SupplementaryGroups containing bare 'jabali' — JAB-357"
    echo "      SupplementaryGroups=$supp"
    fail=1
  fi
done

# 3. The upgrade converger must exist AND run on `jabali update`.
if ! grep -qE '^ensure_stalwart_not_in_panel_group\(\)' install.sh; then
  echo "FAIL: ensure_stalwart_not_in_panel_group() converger missing — upgraded hosts keep the legacy group"
  fail=1
fi
# It must be invoked inside provision_new_software (the `jabali update` sweep).
if ! awk '/^provision_new_software\(\)/{f=1} f&&/ensure_stalwart_not_in_panel_group/{found=1} f&&/^\}/{exit} END{exit !found}' install.sh; then
  echo "FAIL: ensure_stalwart_not_in_panel_group is not called from provision_new_software — the fix never reaches upgraded hosts"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: no non-panel service holds the broad jabali group; upgrade converger wired (JAB-357)"

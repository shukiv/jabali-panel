#!/usr/bin/env bash
# install/tests/test_service_not_in_broad_jabali_group.sh — JAB-357 criteria 1+2.
#
# Rules 1–3 cover the two named non-panel services and their upgrade
# convergers. Rules 4–6 cover every other identity: no installer membership
# command, no systemd unit, and no agent membership grant may give the broad
# group to anyone but the panel account. The live counterpart is
# test_agent_socket_identity_matrix_runtime.sh.
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

# Same for webmail (JAB-351/357): its convergence used to live only inside
# install_bulwark, which is gated behind the mail module (run_if_mail) and is
# NOT reached by a plain `jabali update`. It must have its own converger wired
# into provision_new_software, exactly like Stalwart — else an upgraded webmail
# host keeps the broad jabali group until the mail module is reinstalled.
if ! grep -qE '^ensure_webmail_not_in_panel_group\(\)' install.sh; then
  echo "FAIL: ensure_webmail_not_in_panel_group() converger missing — upgraded webmail hosts keep the legacy group"
  fail=1
fi
if ! awk '/^provision_new_software\(\)/{f=1} f&&/ensure_webmail_not_in_panel_group/{found=1} f&&/^\}/{exit} END{exit !found}' install.sh; then
  echo "FAIL: ensure_webmail_not_in_panel_group is not called from provision_new_software — a plain 'jabali update' never converges webmail"
  fail=1
fi

# ---------------------------------------------------------------------------
# Rules 4–6 widen the check from the two named services to EVERY identity
# (JAB-357 AC1: webmail, mail, PHP, tenant, Redis-client and any other
# service). The panel account is the only member the broad group may have.
# $SERVICE_USER is the panel account's name in install.sh, so it counts as
# the group name too.
panel_group_re='^(jabali|\$SERVICE_USER|\$\{SERVICE_USER\})$'
is_panel_group() { [[ "$1" =~ $panel_group_re ]]; }

# 4. No membership command in install.sh grants the broad group to anyone:
#    usermod -G/-aG <groups> <user>, gpasswd -a <user> <group>,
#    adduser <user> <group>.
while IFS= read -r hit; do
  lineno=${hit%%:*}
  body=${hit#*:}
  [[ "$body" =~ ^[[:space:]]*# ]] && continue
  read -r -a words <<<"${body//[\"\']/}"
  groups=()
  for ((i = 0; i < ${#words[@]}; i++)); do
    case "${words[i]}" in
      usermod)
        for ((j = i + 1; j < ${#words[@]}; j++)); do
          case "${words[j]}" in
            -G | -aG | -Ga | --groups) IFS=, read -r -a g <<<"${words[j+1]:-}"; groups+=("${g[@]}") ;;
          esac
        done ;;
      gpasswd)
        [[ "${words[i+1]:-}" == "-a" ]] && groups+=("${words[i+3]:-}") ;;
      adduser)
        [[ -n "${words[i+1]:-}" && "${words[i+1]}" != -* && -n "${words[i+2]:-}" && "${words[i+2]}" != -* ]] &&
          groups+=("${words[i+2]}") ;;
    esac
  done
  for g in "${groups[@]}"; do
    if is_panel_group "$g"; then
      echo "FAIL: install.sh:$lineno grants the broad jabali group to an account — JAB-357 AC1"
      echo "      $body"
      fail=1
    fi
  done
done < <(grep -nE '\b(usermod|gpasswd|adduser)\b' install.sh)

# 5. No systemd unit grants the broad group to a user other than the panel
#    account or root — both the unit files in install/systemd/ and the units
#    install.sh writes inline. A grant is `Group=` or a bare token in
#    `SupplementaryGroups=`; the unit's user is the nearest `User=` above it
#    within the same unit (no `User=` means root).
unit_grants() {
  awk -v file="$1" '
    /^\[Unit\]/ { user = "" }
    /^User=/ { user = substr($0, 6); gsub(/["\047]/, "", user) }
    /^(Group|SupplementaryGroups)=/ {
      val = $0; sub(/^[^=]*=/, "", val); gsub(/["\047]/, "", val)
      n = split(val, toks, /[ \t]+/)
      for (i = 1; i <= n; i++) {
        t = toks[i]
        if (t == "jabali" || t == "$SERVICE_USER" || t == "${SERVICE_USER}") {
          if (user != "" && user != "root" && user != "jabali" && user != "$SERVICE_USER" && user != "${SERVICE_USER}")
            printf "%s:%d: User=%s %s\n", file, NR, user, $0
        }
      }
    }' "$1"
}
for unit in install/systemd/*.service install.sh; do
  [[ -f "$unit" ]] || continue
  while IFS= read -r g; do
    [[ -n "$g" ]] || continue
    echo "FAIL: a unit grants the broad jabali group to a non-panel user — JAB-357 AC1"
    echo "      $g"
    fail=1
  done < <(unit_grants "$unit")
done

# 6. Every membership grant the agent makes is to a reviewed group. The agent
#    adds tenant accounts to groups at runtime (SFTP, FTP, WebDAV, SSH
#    sandbox, Redis clients) and the system restore re-adds backed-up
#    memberships. A new `usermod -aG` call site fails here until it is
#    reviewed and listed; a reviewed name must never resolve to the broad
#    group. The restore's a.group is filtered by installerManagedGroups,
#    which must keep "jabali" (the restore never adds members to it).
reviewed_agent_groups=(sftpGroupName ftpGroupName webdavGroupName sandboxGroupName
  forwardGroupName redisClientsGroup '"jabali-redis-clients"' a.group)
agent_src=panel-agent/internal/commands
while IFS= read -r hit; do
  arg=$(sed -E 's/.*"usermod", "-aG", ([^,]+),.*/\1/' <<<"$hit")
  reviewed=0
  for r in "${reviewed_agent_groups[@]}"; do [[ "$arg" == "$r" ]] && reviewed=1; done
  if [[ "$reviewed" -eq 0 ]]; then
    echo "FAIL: unreviewed agent membership grant (group arg '$arg') — review it against JAB-357 and list it here"
    echo "      $hit"
    fail=1
    continue
  fi
  [[ "$arg" == a.group ]] && continue # data-driven; guarded by installerManagedGroups below
  if [[ "$arg" == \"*\" ]]; then
    value=${arg//\"/}
  else
    value=$(grep -hE "^[[:space:]]*(const[[:space:]]+)?${arg}[[:space:]]*=[[:space:]]*\"" "$agent_src"/*.go |
      head -1 | sed -E 's/.*"([^"]*)".*/\1/' || true)
  fi
  if [[ -z "$value" ]]; then
    echo "FAIL: cannot resolve the agent group constant $arg — a reviewed grant must name a known group"
    echo "      $hit"
    fail=1
  elif [[ "$value" == "jabali" ]]; then
    echo "FAIL: agent grants tenant accounts the broad jabali group via $arg — JAB-357 AC1"
    echo "      $hit"
    fail=1
  fi
done < <(grep -nE '"usermod", "-aG",' "$agent_src"/*.go | grep -v '_test\.go:')
if grep -nE '"gpasswd", "-a",' "$agent_src"/*.go | grep -v '_test\.go:' | grep -q .; then
  echo "FAIL: agent adds group members with gpasswd -a — route it through a reviewed usermod -aG call site"
  fail=1
fi
restore_src="$agent_src/backup_system_os_users_apply.go"
if ! awk '/^var installerManagedGroups = /{f=1} f&&/"jabali":/{found=1} f&&/^}/{exit} END{exit !found}' "$restore_src"; then
  echo "FAIL: installerManagedGroups in $restore_src no longer lists \"jabali\" — a system restore could re-add members to the broad group"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: no non-panel service holds the broad jabali group; upgrade converger wired (JAB-357)"

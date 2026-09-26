#!/usr/bin/env bash
# install/tests/test_agent_socket_identity_matrix_runtime.sh — JAB-357 AC1/AC6
# runtime socket matrix.
#
# The root Agent socket (/run/jabali/agent.sock, inside /run/jabali 0750
# root:jabali) does no caller authentication beyond two layers: the
# filesystem (only the jabali group may traverse /run/jabali) and the
# SO_PEERCRED UID allow-list (only the panel user and root). This test proves
# both layers on a live host, from the real identities that must NOT reach it:
#
#   webmail   jabali-webmail.service — probed inside its own mount namespace
#   mail      jabali-stalwart.service
#   redis     redis-server.service
#   nginx     a www-data worker
#   php       a PHP-FPM pool worker running as a tenant uid
#
# Each probe borrows the EXACT uid, gid and supplementary groups of the live
# process (from /proc/<pid>/status) and enters its mount namespace, so a
# group granted by a systemd unit's SupplementaryGroups= is exercised too —
# that grant never shows up in /etc/group. The probe only connects and reads
# the agent's first reply; it never sends a command.
#
# Outcomes per non-panel identity:
#   EACCES / ENOENT  pass — the filesystem layer stopped it.
#   DENIED           FAIL — it reached the socket and only the SO_PEERCRED
#                    gate stopped it; the group layer has regressed.
#   ACCEPTED         FAIL — it can drive the root Agent.
# The panel service is the positive control and must be ACCEPTED, so a broken
# probe can never pass as "everything blocked".
#
# This is a BOX verification. In CI (not root, no agent socket) it SKIPs with
# exit 0 so the install.sh regression gate stays green.
#
#     sudo bash install/tests/test_agent_socket_identity_matrix_runtime.sh
set -uo pipefail   # NOT -e: each row decides skip-vs-fail explicitly

SOCK=/run/jabali/agent.sock

skip() { echo "SKIP: $*"; exit 0; }

[ "$(id -u)" -eq 0 ] || skip "needs root to enter the live services' credentials"
[ -S "$SOCK" ] || skip "no agent socket at $SOCK (not an installed host)"
for bin in nsenter setpriv python3; do
  command -v "$bin" >/dev/null 2>&1 || skip "$bin not present"
done

# Connect, then read the agent's first reply. A rejected peer gets an
# immediate permission_denied error line; an allowed peer gets nothing until
# it sends a request, so a read timeout means the connection was accepted.
# shellcheck disable=SC2016
PROBE='
import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(3)
try:
    s.connect(sys.argv[1])
except PermissionError:
    print("EACCES"); sys.exit(0)
except FileNotFoundError:
    print("ENOENT"); sys.exit(0)
except OSError as e:
    print("ERR " + (e.strerror or str(e))); sys.exit(0)
try:
    data = s.recv(4096)
except socket.timeout:
    print("ACCEPTED"); sys.exit(0)
print("DENIED" if b"permission_denied" in data else "OTHER " + data[:120].decode("utf-8", "replace"))
'

# probe_as_pid PID — run the probe with the live process's uid, gid and
# supplementary groups, inside its mount namespace. No capabilities.
probe_as_pid() {
  local pid=$1 uid gid groups
  uid=$(awk '/^Uid:/{print $2}' "/proc/$pid/status" 2>/dev/null)
  gid=$(awk '/^Gid:/{print $2}' "/proc/$pid/status" 2>/dev/null)
  groups=$(awk '/^Groups:/{$1=""; sub(/^ +/, ""); sub(/ +$/, ""); gsub(/ +/, ","); print}' "/proc/$pid/status" 2>/dev/null)
  if [ -z "$uid" ] || [ -z "$gid" ]; then
    echo "ERR cannot read /proc/$pid/status"
    return
  fi
  local gflag=(--clear-groups)
  [ -n "$groups" ] && gflag=(--groups "$groups")
  local drop=(setpriv --reuid "$uid" --regid "$gid" "${gflag[@]}" --inh-caps=-all --bounding-set=-all --)
  local out
  out=$(nsenter --target "$pid" --mount -- "${drop[@]}" python3 -c "$PROBE" "$SOCK" 2>&1 | tail -1)
  case "$out" in
    nsenter:*"failed to execute"*)
      # The service's namespace forbids exec (NoExecPaths, e.g. Redis). Probe
      # with the same credentials from the host namespace instead: a service
      # namespace can only hide more of the filesystem, never less, so this
      # is the stricter test of the credential layer.
      out="HOSTNS $("${drop[@]}" python3 -c "$PROBE" "$SOCK" 2>&1 | tail -1)" ;;
  esac
  echo "$out"
}

unit_pid() {
  local pid
  pid=$(systemctl show "$1" -p MainPID --value 2>/dev/null)
  [ -n "$pid" ] && [ "$pid" != 0 ] && echo "$pid"
}

fails=0
checked=0

# check LABEL PID EXPECT — EXPECT is "blocked" or "accepted".
check() {
  local label=$1 pid=$2 expect=$3 out
  if [ -z "$pid" ]; then
    echo "SKIP: $label — not running on this host"
    return
  fi
  out=$(probe_as_pid "$pid")
  case "$out" in
    HOSTNS\ *) out=${out#HOSTNS }; label="$label [host mount namespace: its own forbids exec]" ;;
  esac
  checked=$((checked + 1))
  case "$expect:$out" in
    blocked:EACCES | blocked:ENOENT)
      echo "OK:   $label (pid $pid) — filesystem layer refused the connect ($out)" ;;
    blocked:DENIED)
      echo "FAIL: $label (pid $pid) reached the socket; only the SO_PEERCRED gate stopped it — the jabali group layer has regressed"
      fails=$((fails + 1)) ;;
    blocked:ACCEPTED)
      echo "FAIL: $label (pid $pid) CAN DRIVE THE ROOT AGENT — both layers are open"
      fails=$((fails + 1)) ;;
    accepted:ACCEPTED)
      echo "OK:   $label (pid $pid) — positive control accepted" ;;
    *)
      echo "FAIL: $label (pid $pid) — expected $expect, probe said: ${out:-<nothing>}"
      fails=$((fails + 1)) ;;
  esac
}

check "webmail (jabali-webmail, own mount namespace)" "$(unit_pid jabali-webmail)" blocked
check "mail (jabali-stalwart)" "$(unit_pid jabali-stalwart)" blocked
check "redis (redis-server)" "$(unit_pid redis-server)" blocked
check "nginx worker (www-data)" "$(pgrep -u www-data -x nginx 2>/dev/null | head -1)" blocked
check "PHP-FPM tenant pool worker" "$(ps -eo pid=,uid=,comm= 2>/dev/null |
  awk '$2 >= 1000 && $2 < 60000 && $3 ~ /^php-fpm/ {print $1; exit}')" blocked
check "panel (jabali-panel) — positive control" "$(unit_pid jabali-panel)" accepted

if [ "$checked" -eq 0 ]; then
  skip "no identity was running to probe"
fi
if [ "$fails" -ne 0 ]; then
  echo "RESULT: FAIL ($fails of $checked probes)"
  exit 1
fi
echo "PASS: agent socket identity matrix — $checked probes (JAB-357 AC1/AC6)"

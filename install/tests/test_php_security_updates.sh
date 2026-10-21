#!/usr/bin/env bash
# install/tests/test_php_security_updates.sh — PHP security releases reach a
# Jabali box, and the per-user FPM masters then run the patched PHP.
#
# PHP comes from Sury (packages.sury.org, ppa:ondrej/php). Stock
# unattended-upgrades allows only the distro's origins, so the October 21 2026
# PHP releases (CVE-2026-109618, CVE-2026-109619) reached no box by itself.
# Upgrading the packages restarts only the masked php<v>-fpm.service; the
# jabali-fpm@<user> masters kept running the replaced binary.
#
# 1. Static: the unattended-upgrades drop-in allows both Sury origins, and the
#    apt hook runs the refresh script without ever failing apt.
# 2. Functional: jabali-php-fpm-refresh, with systemctl and readlink stubbed,
#    reloads exactly the masters whose binary was replaced, falls back to a
#    restart, and skips other units and bad PIDs.
# 3. Functional: ensure_php_security_updates writes both drop-ins and the
#    script; with upgrade_now=1 it upgrades only the installed php packages,
#    keeping config files; without it, it upgrades nothing.
# 4. Static: fresh install and `jabali update` call it (update with the
#    upgrade); uninstall removes its files.
#
# Run from repo root:
#     bash install/tests/test_php_security_updates.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
script=install/php/jabali-php-fpm-refresh
fn_src=$(awk '/^ensure_php_security_updates\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_php_security_updates not defined in install.sh"
  exit 1
fi
if [[ ! -x "$script" ]]; then
  echo "FAIL: $script missing or not executable"
  exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# --- 2. The refresh script. ---
# Fake units: a=replaced binary, b=current binary, c=replaced but reload fails,
# d=bad MainPID, nginx.service must be ignored.
bin="$tmpdir/bin"
mkdir -p "$bin"
calls="$tmpdir/calls"
cat >"$bin/systemctl" <<'EOF'
#!/bin/bash
echo "systemctl $*" >>"$CALLS"
case "$1" in
  list-units)
    printf '%s\n' \
      "jabali-fpm@a.service loaded active running a" \
      "jabali-fpm@b.service loaded active running b" \
      "jabali-fpm@c.service loaded active running c" \
      "jabali-fpm@d.service loaded active running d" \
      "nginx.service loaded active running n" ;;
  show)
    case "${@: -1}" in
      jabali-fpm@a.service) echo 101 ;;
      jabali-fpm@b.service) echo 102 ;;
      jabali-fpm@c.service) echo 103 ;;
      jabali-fpm@d.service) echo 0 ;;
      nginx.service) echo 104 ;;
    esac ;;
  reload) [[ "$2" != jabali-fpm@c.service ]] ;;
  restart) exit 0 ;;
esac
EOF
cat >"$bin/readlink" <<'EOF'
#!/bin/bash
case "$1" in
  /proc/101/exe) echo "/usr/sbin/php-fpm8.4 (deleted)" ;;
  /proc/102/exe) echo "/usr/sbin/php-fpm8.4" ;;
  /proc/103/exe) echo "/usr/sbin/php-fpm8.3 (deleted)" ;;
  /proc/104/exe) echo "/usr/sbin/nginx (deleted)" ;;
  # Unit d has MainPID 0 (not running): the script must not even look.
  /proc/0/exe) echo "/usr/sbin/php-fpm8.4 (deleted)" ;;
  *) exit 1 ;;
esac
EOF
printf '#!/bin/bash\nexit 0\n' >"$bin/logger"
chmod +x "$bin"/*
: >"$calls"
if ! CALLS="$calls" PATH="$bin:$PATH" bash "$script"; then
  echo "FAIL: $script exited non-zero (it must never fail apt)"
  fail=1
fi
grep -qx 'systemctl reload jabali-fpm@a.service' "$calls" \
  || { echo "FAIL: a master running a replaced binary was not reloaded"; fail=1; }
if grep -qE '^systemctl (reload|restart) jabali-fpm@b\.service$' "$calls"; then
  echo "FAIL: a master on the current binary was reloaded"
  fail=1
fi
grep -qx 'systemctl restart jabali-fpm@c.service' "$calls" \
  || { echo "FAIL: a failed reload did not fall back to a restart"; fail=1; }
if grep -qE '^systemctl (reload|restart) (jabali-fpm@d|nginx)\.service$' "$calls"; then
  echo "FAIL: reloaded a unit with no PID, or a non-FPM unit"
  fail=1
fi
grep -q -- "--state=running jabali-fpm@\*.service" "$calls" \
  || { echo "FAIL: the script does not list the jabali-fpm@ units"; fail=1; }

# --- 3. The converger, against temp paths. ---
root="$tmpdir/root"
mkdir -p "$root/etc/apt/apt.conf.d" "$root/usr/local/sbin"
export REPO_DIR="$tmpdir/repo"
mkdir -p "$REPO_DIR/install/php"
cp "$script" "$REPO_DIR/install/php/"
# Re-point only the path variables; the hook text must keep the real path.
fn_tmp="${fn_src//local refresh=\/usr\/local\/sbin\//local refresh=$root/usr/local/sbin/}"
fn_tmp="${fn_tmp//local uu_conf=\/etc\/apt\//local uu_conf=$root/etc/apt/}"
fn_tmp="${fn_tmp//local hook_conf=\/etc\/apt\//local hook_conf=$root/etc/apt/}"
fn_tmp="${fn_tmp//install -d -m 0755 \/usr\/local\/sbin \/etc\/apt\/apt.conf.d/:}"
if [[ "$fn_tmp" == *'local refresh=/usr/local'* || "$fn_tmp" == *'local uu_conf=/etc'* || "$fn_tmp" == *'local hook_conf=/etc'* ]]; then
  echo "FAIL: could not re-point ensure_php_security_updates at temp paths (variable names changed?)"
  exit 1
fi
apt_calls="$tmpdir/apt-calls"
run_converger() { # [upgrade_now]
  : >"$apt_calls"
  (
    _ok() { :; }
    _warn() { :; }
    _log() { :; }
    install() { local a=("$@"); cp "${a[-2]}" "${a[-1]}"; chmod 0755 "${a[-1]}"; }
    dpkg-query() { printf '%s\n' 'ii  php8.4-fpm' 'ii  php8.4-common' 'un  php8.2-fpm' 'rc  php8.1-fpm' 'ii  php-common'; }
    apt-get() { echo "apt-get $*" >>"$apt_calls"; }
    timeout() { shift; "$@"; }
    eval "$fn_tmp"
    ensure_php_security_updates "$@"
  )
}
# The converger runs the installed refresh script at the end: stub it out.
run_with_stub_refresh() {
  CALLS="$tmpdir/refresh-calls" PATH="$bin:$PATH" run_converger "$@"
}

run_with_stub_refresh 1
uu="$root/etc/apt/apt.conf.d/52jabali-php-updates"
hook="$root/etc/apt/apt.conf.d/99jabali-php-fpm-refresh"
for want in 'Unattended-Upgrade::Origins-Pattern {' \
            '"origin=deb.sury.org,codename=${distro_codename}";' \
            '"origin=LP-PPA-ondrej-php,codename=${distro_codename}";'; do
  grep -qF -- "$want" "$uu" 2>/dev/null || { echo "FAIL: $uu missing: $want"; fail=1; }
done
grep -qF 'DPkg::Post-Invoke { "[ -x /usr/local/sbin/jabali-php-fpm-refresh ] && /usr/local/sbin/jabali-php-fpm-refresh || true"; };' "$hook" 2>/dev/null \
  || { echo "FAIL: $hook does not run the refresh script, or can fail apt"; fail=1; }
cmp -s "$script" "$root/usr/local/sbin/jabali-php-fpm-refresh" \
  || { echo "FAIL: the refresh script was not installed"; fail=1; }
inst=$(grep '^apt-get install' "$apt_calls" || true)
if [[ -z "$inst" ]]; then
  echo "FAIL: upgrade_now=1 did not upgrade PHP"
  fail=1
else
  for want in '--only-upgrade' '-o Dpkg::Options::=--force-confold' ' php8.4-fpm' ' php8.4-common' ' php-common'; do
    grep -qF -- "$want" <<<"$inst" || { echo "FAIL: PHP upgrade lacks '$want': $inst"; fail=1; }
  done
  if grep -qE 'php8\.2-fpm|php8\.1-fpm' <<<"$inst"; then
    echo "FAIL: PHP upgrade names packages that are not installed: $inst"
    fail=1
  fi
fi
grep -q '^apt-get update' "$apt_calls" || { echo "FAIL: upgrade_now=1 did not refresh the package lists"; fail=1; }

run_with_stub_refresh
if [[ -s "$apt_calls" ]]; then
  echo "FAIL: without upgrade_now the converger ran apt-get: $(cat "$apt_calls")"
  fail=1
fi

rm -rf "$REPO_DIR/install/php" "$root/usr/local/sbin/jabali-php-fpm-refresh"
run_with_stub_refresh 1
if [[ -e "$root/usr/local/sbin/jabali-php-fpm-refresh" ]] || [[ -s "$apt_calls" ]]; then
  echo "FAIL: with the script missing from the repo the converger still acted"
  fail=1
fi

# --- 4. Call sites and uninstall. ---
main_src=$(awk '/^main\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE '^[[:space:]]+ensure_php_security_updates([[:space:]]+#.*)?$' <<<"$main_src"; then
  echo "FAIL: main() (fresh install) does not call ensure_php_security_updates"
  fail=1
fi
upd_src=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE '^[[:space:]]+ensure_php_security_updates 1$' <<<"$upd_src"; then
  echo "FAIL: provision_new_software (jabali update) does not call ensure_php_security_updates 1"
  fail=1
fi
for f in /etc/apt/apt.conf.d/52jabali-php-updates /etc/apt/apt.conf.d/99jabali-php-fpm-refresh /usr/local/sbin/jabali-php-fpm-refresh; do
  grep -qF "rm -f  $f" install.sh || { echo "FAIL: uninstall does not remove $f"; fail=1; }
done

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: PHP updates reach the box and the FPM masters reload onto them"

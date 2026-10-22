#!/usr/bin/env bash
# install/tests/test_security_updates.sh — security updates from the vendor
# repositories Jabali adds reach a box by themselves, and the internet-facing
# services then run the patched code.
#
# Stock unattended-upgrades allows only the distro's origins, so the October 21
# 2026 PHP releases (CVE-2026-109618, CVE-2026-109619) reached no box by itself,
# and neither did a CrowdSec, NodeSource or Docker update. A package upgrade
# also leaves running processes on the old files: the per-user PHP-FPM masters
# after a PHP upgrade (php<v>-fpm.service is masked), nginx and PHP after an
# OpenSSL upgrade.
#
# 1. Functional: jabali-service-refresh, against a fake /proc and a stubbed
#    systemctl, reloads PHP-FPM masters and restarts nginx, webmail and
#    PowerDNS when their executable or a mapped library was replaced, and
#    leaves everything else alone.
# 2. Functional: ensure_security_updates writes the periodic, origins and hook
#    drop-ins and the script, removes the PHP-only files it replaces, and with
#    upgrade_now=1 upgrades only the installed php packages.
# 3. Static: fresh install and `jabali update` call it (update with the
#    upgrade); uninstall removes its files.
#
# Run from repo root:
#     bash install/tests/test_security_updates.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
script=install/apt/jabali-service-refresh
fn_src=$(awk '/^ensure_security_updates\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_security_updates not defined in install.sh"
  exit 1
fi
if [[ ! -x "$script" ]]; then
  echo "FAIL: $script missing or not executable"
  exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# --- 1. The refresh script. ---
# Units and their MainPIDs:
#   fpm@a 101  exe replaced                      -> reload
#   fpm@b 102  current exe, libssl replaced      -> reload
#   fpm@c 103  current, only memfd/tmp deleted   -> nothing
#   fpm@d 104  exe replaced, reload fails        -> restart
#   fpm@e 0    not running                       -> nothing
#   nginx 201  libssl replaced                   -> restart, never reload
#   jabali-webmail 202  node replaced            -> restart
#   pdns 203   current                           -> nothing
#   pdns-recursor 204  libcrypto replaced        -> restart
#   crowdsec 205  exe replaced, not covered      -> nothing
fakeproc="$tmpdir/proc"
mkproc() { # <pid> <exe target> [maps line...]
  local pid=$1 exe=$2
  shift 2
  mkdir -p "$fakeproc/$pid"
  ln -s "$exe" "$fakeproc/$pid/exe"
  : >"$fakeproc/$pid/maps"
  local l
  for l in "$@"; do printf '7f0000000000-7f0000001000 r-xp 00000000 fd:01 1234                       %s\n' "$l" >>"$fakeproc/$pid/maps"; done
}
mkproc 101 "/usr/sbin/php-fpm8.4 (deleted)"
mkproc 102 /usr/sbin/php-fpm8.4 /usr/sbin/php-fpm8.4 "/usr/lib/x86_64-linux-gnu/libssl.so.3 (deleted)"
mkproc 103 /usr/sbin/php-fpm8.3 /usr/sbin/php-fpm8.3 "/memfd:jit (deleted)" "/tmp/.ZendSem.x (deleted)" "/dev/shm/opcache (deleted)"
mkproc 104 "/usr/sbin/php-fpm8.4 (deleted)"
mkproc 0 "/usr/sbin/php-fpm8.4 (deleted)"
mkproc 201 /usr/sbin/nginx /usr/sbin/nginx "/usr/lib/x86_64-linux-gnu/libssl.so.3 (deleted)"
mkproc 202 "/usr/bin/node (deleted)"
mkproc 203 /usr/sbin/pdns_server /usr/sbin/pdns_server /usr/lib/x86_64-linux-gnu/libcrypto.so.3
mkproc 204 /usr/sbin/pdns_recursor /usr/sbin/pdns_recursor "/lib/x86_64-linux-gnu/libcrypto.so.3 (deleted)"
mkproc 205 "/usr/bin/crowdsec (deleted)"

bin="$tmpdir/bin"
mkdir -p "$bin"
calls="$tmpdir/calls"
cat >"$bin/systemctl" <<'EOF'
#!/bin/bash
echo "systemctl $*" >>"$CALLS"
case "$1" in
  list-units)
    printf '%s loaded active running x\n' \
      jabali-fpm@a.service jabali-fpm@b.service jabali-fpm@c.service \
      jabali-fpm@d.service jabali-fpm@e.service nginx.service \
      jabali-webmail.service pdns.service pdns-recursor.service crowdsec.service ;;
  show)
    case "${@: -1}" in
      jabali-fpm@a.service) echo 101 ;;
      jabali-fpm@b.service) echo 102 ;;
      jabali-fpm@c.service) echo 103 ;;
      jabali-fpm@d.service) echo 104 ;;
      jabali-fpm@e.service) echo 0 ;;
      nginx.service) echo 201 ;;
      jabali-webmail.service) echo 202 ;;
      pdns.service) echo 203 ;;
      pdns-recursor.service) echo 204 ;;
      crowdsec.service) echo 205 ;;
    esac ;;
  reload) [[ "$2" != jabali-fpm@d.service ]] ;;
  restart) exit 0 ;;
esac
EOF
printf '#!/bin/bash\nexit 0\n' >"$bin/logger"
chmod +x "$bin"/*
: >"$calls"
if ! CALLS="$calls" JABALI_PROC="$fakeproc" PATH="$bin:$PATH" bash "$script"; then
  echo "FAIL: $script exited non-zero (it must never fail apt)"
  fail=1
fi
want_call() { grep -qx "systemctl $1" "$calls" || { echo "FAIL: missing 'systemctl $1' — $2"; fail=1; }; }
no_call() { if grep -qE "^systemctl (reload|restart) $1\$" "$calls"; then echo "FAIL: $1 was reloaded or restarted — $2"; fail=1; fi; }
want_call "reload jabali-fpm@a.service" "a master on a replaced PHP binary"
want_call "reload jabali-fpm@b.service" "a master on a replaced libssl"
no_call "jabali-fpm@c\.service" "memfd, /tmp and /dev/shm files are not package files"
want_call "restart jabali-fpm@d.service" "a failed reload must fall back to a restart"
no_call "jabali-fpm@e\.service" "a unit with no running process"
want_call "restart nginx.service" "nginx on a replaced libssl"
if grep -qx "systemctl reload nginx.service" "$calls"; then
  echo "FAIL: nginx was reloaded; a reload keeps the old master and its libraries"
  fail=1
fi
want_call "restart jabali-webmail.service" "webmail on a replaced node"
no_call "pdns\.service" "PowerDNS on current files"
want_call "restart pdns-recursor.service" "the recursor on a replaced libcrypto under /lib"
no_call "crowdsec\.service" "only the listed services are refreshed"
grep -q -- "--state=running jabali-fpm@\*.service nginx.service jabali-webmail.service pdns.service pdns-recursor.service" "$calls" \
  || { echo "FAIL: the script does not list the units it covers"; fail=1; }

# --- 2. The converger, against temp paths. ---
root="$tmpdir/root"
mkdir -p "$root/etc/apt/apt.conf.d" "$root/usr/local/sbin"
export REPO_DIR="$tmpdir/repo"
mkdir -p "$REPO_DIR/install/apt"
cp "$script" "$REPO_DIR/install/apt/"
# Re-point the path variables (the `local` lines) only; the hook text must keep
# the real path.
fn_tmp=$(sed -E "/^  local /s#(=|[\" ])(/etc/apt/|/usr/local/sbin/)#\\1$root\\2#g" <<<"$fn_src")
fn_tmp="${fn_tmp//install -d -m 0755 \/usr\/local\/sbin \/etc\/apt\/apt.conf.d/:}"
if grep -E '^  local ' <<<"$fn_tmp" | grep -qE '(=|[" ])/(etc|usr/local)/'; then
  echo "FAIL: could not re-point ensure_security_updates at temp paths"
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
    ensure_security_updates "$@"
  )
}
# The converger runs the installed refresh script at the end: give it the stubs.
run_with_stubs() {
  CALLS="$tmpdir/refresh-calls" JABALI_PROC="$fakeproc" PATH="$bin:$PATH" run_converger "$@"
}

# The first version's PHP-only files are on the box and must go.
old_uu="$root/etc/apt/apt.conf.d/52jabali-php-updates"
old_hook="$root/etc/apt/apt.conf.d/99jabali-php-fpm-refresh"
old_script="$root/usr/local/sbin/jabali-php-fpm-refresh"
touch "$old_uu" "$old_hook" "$old_script"

run_with_stubs 1
periodic="$root/etc/apt/apt.conf.d/10jabali-auto-upgrades"
uu="$root/etc/apt/apt.conf.d/52jabali-security-updates"
hook="$root/etc/apt/apt.conf.d/99jabali-service-refresh"
for want in 'APT::Periodic::Update-Package-Lists "1";' 'APT::Periodic::Unattended-Upgrade "1";'; do
  grep -qxF -- "$want" "$periodic" 2>/dev/null || { echo "FAIL: $periodic missing: $want"; fail=1; }
done
for want in 'Unattended-Upgrade::Origins-Pattern {' \
            '"origin=deb.sury.org,codename=${distro_codename}";' \
            '"origin=LP-PPA-ondrej-php,codename=${distro_codename}";' \
            '"origin=packagecloud.io/crowdsec/crowdsec,site=packagecloud.io";' \
            '"site=deb.nodesource.com";' \
            '"origin=Docker,site=download.docker.com";'; do
  grep -qF -- "$want" "$uu" 2>/dev/null || { echo "FAIL: $uu missing: $want"; fail=1; }
done
# unattended-upgrades matches each Package-Blacklist entry from the start of the
# package name (Python re.match): only the Docker daemon is held back.
mapfile -t held < <(sed -n '/^Unattended-Upgrade::Package-Blacklist {/,/^};/s/^ *"\(.*\)";$/\1/p' "$uu" 2>/dev/null)
blacklisted() {
  local re
  for re in "${held[@]}"; do [[ "$1" =~ ^$re ]] && return 0; done
  return 1
}
blacklisted docker-ce || { echo "FAIL: unattended-upgrades may upgrade the Docker daemon (docker-ce)"; fail=1; }
for pkg in docker-ce-cli docker-ce-rootless-extras docker-buildx-plugin docker-compose-plugin \
           containerd.io nodejs crowdsec php8.4-fpm; do
  blacklisted "$pkg" && { echo "FAIL: unattended-upgrades holds back $pkg"; fail=1; }
done
grep -qxF 'DPkg::Post-Invoke { "[ -x /usr/local/sbin/jabali-service-refresh ] && /usr/local/sbin/jabali-service-refresh || true"; };' "$hook" 2>/dev/null \
  || { echo "FAIL: $hook does not run the refresh script, or can fail apt"; fail=1; }
cmp -s "$script" "$root/usr/local/sbin/jabali-service-refresh" \
  || { echo "FAIL: the refresh script was not installed"; fail=1; }
for f in "$old_uu" "$old_hook" "$old_script"; do
  [[ ! -e "$f" ]] || { echo "FAIL: the PHP-only file it replaces is still there: $f"; fail=1; }
done
grep -qx "systemctl reload jabali-fpm@a.service" "$tmpdir/refresh-calls" 2>/dev/null \
  || { echo "FAIL: the converger did not run the refresh script"; fail=1; }
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

run_with_stubs
if [[ -s "$apt_calls" ]]; then
  echo "FAIL: without upgrade_now the converger ran apt-get: $(cat "$apt_calls")"
  fail=1
fi

rm -rf "$REPO_DIR/install/apt" "$root/usr/local/sbin/jabali-service-refresh" "$periodic" "$uu" "$hook"
run_with_stubs 1
if [[ -e "$root/usr/local/sbin/jabali-service-refresh" || -e "$uu" || -s "$apt_calls" ]]; then
  echo "FAIL: with the script missing from the repo the converger still acted"
  fail=1
fi

# --- 3. Call sites and uninstall. ---
main_src=$(awk '/^main\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE '^[[:space:]]+ensure_security_updates([[:space:]]+#.*)?$' <<<"$main_src"; then
  echo "FAIL: main() (fresh install) does not call ensure_security_updates"
  fail=1
fi
upd_src=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE '^[[:space:]]+ensure_security_updates 1$' <<<"$upd_src"; then
  echo "FAIL: provision_new_software (jabali update) does not call ensure_security_updates 1"
  fail=1
fi
for f in /etc/apt/apt.conf.d/10jabali-auto-upgrades /etc/apt/apt.conf.d/52jabali-security-updates \
         /etc/apt/apt.conf.d/99jabali-service-refresh /usr/local/sbin/jabali-service-refresh \
         /etc/apt/apt.conf.d/52jabali-php-updates /etc/apt/apt.conf.d/99jabali-php-fpm-refresh \
         /usr/local/sbin/jabali-php-fpm-refresh; do
  grep -qF "rm -f  $f" install.sh || { echo "FAIL: uninstall does not remove $f"; fail=1; }
done

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: vendor security updates reach the box and the exposed services reload onto them"

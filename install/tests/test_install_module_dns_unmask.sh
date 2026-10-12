#!/usr/bin/env bash
# Turning DNS on works when an earlier update masked PowerDNS (GH #2101).
#
# converge_pdns_masking (GH #447) masks pdns and pdns-recursor whenever a full
# install or `jabali update` runs while server_settings.dns_enabled is 0. A
# masked unit can't start, so turning DNS on in Server Settings, or Retry,
# which run `install.sh --install-module dns`, died in install_powerdns on
# "Unit pdns.service is masked" until a nightly update unmasked it.
# install_module_dns now unmasks both units before the config functions start
# them.
#
# Run from repo root:
#     bash install/tests/test_install_module_dns_unmask.sh
set -uo pipefail

INSTALL_SH="${INSTALL_SH:-install.sh}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn_src=$(awk '/^install_module_dns\(\) \{$/,/^\}$/' "$INSTALL_SH")
if [[ -z "$fn_src" ]]; then
  echo "FAIL: install_module_dns not defined in $INSTALL_SH"
  echo "RESULT: FAIL"
  exit 1
fi

log="$tmp/calls.log"
masked="$tmp/masked"

# A host where both units are masked and stopped, with the packages present.
# systemctl refuses to start a masked unit, as the real one does.
cat >"$tmp/run.sh" <<EOF
set -e
log="$log"
masked="$masked"
EOF
cat >>"$tmp/run.sh" <<'EOF'
printf '%s\n' pdns.service pdns-recursor.service >"$masked"
systemctl() {
  echo "systemctl $*" >>"$log"
  case "$1" in
    unmask)
      shift
      local u
      for u in "$@"; do sed -i "/^${u}\$/d" "$masked"; done
      ;;
    start|restart)
      if grep -qx "${2%.service}.service" "$masked"; then
        echo "Failed to start ${2%.service}.service: Unit ${2%.service}.service is masked." >&2
        return 1
      fi
      ;;
  esac
  return 0
}
dpkg() { return 0; }
reconstruct_server_env_from_db() { echo "reconstruct" >>"$log"; }
_install_dns_packages() { echo "apt" >>"$log"; }
install_powerdns() { echo "install_powerdns" >>"$log"; systemctl start pdns; }
bootstrap_pdns_self_zone() { echo "bootstrap_pdns_self_zone" >>"$log"; }
install_pdns_recursor() { echo "install_pdns_recursor" >>"$log"; systemctl restart pdns-recursor; }
EOF
printf '%s\n' "$fn_src" 'install_module_dns' >>"$tmp/run.sh"

if ! out=$(bash "$tmp/run.sh" 2>&1); then
  echo "FAIL: the DNS install died with pdns masked: $out"
  fail=1
fi
if [[ -s "$masked" ]]; then
  echo "FAIL: still masked after the DNS install: $(tr '\n' ' ' <"$masked")"
  fail=1
fi
unmask_at=$(grep -n -m1 '^systemctl unmask .*pdns.service' "$log" | cut -d: -f1)
config_at=$(grep -n -m1 '^install_powerdns$' "$log" | cut -d: -f1)
if [[ -z "$unmask_at" || -z "$config_at" ]] || (( unmask_at > config_at )); then
  echo "FAIL: pdns must be unmasked before install_powerdns starts it; calls were:"
  sed 's/^/  /' "$log"
  fail=1
fi
if ! grep -q '^systemctl unmask .*pdns-recursor.service' "$log"; then
  echo "FAIL: pdns-recursor was not unmasked; install_pdns_recursor would die the same way"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "RESULT: PASS"
  exit 0
fi
echo "RESULT: FAIL"
exit 1

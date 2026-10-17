#!/usr/bin/env bash
# `jabali update` renders kratos.yml with the panel's own hostname.
#
# The update re-runs install_kratos without JABALI_SRV_HOSTNAME. install_kratos
# then took the hostname from config.toml, which is only the install-time
# seed: on a box renamed before system.set_hostname kept config.toml in step
# it still holds the old name, so every update put Kratos back on the old
# origin (a CORS-blocked login) until the panel's kratos reconciler moved it
# forward again. The server_settings row is the name the panel serves on and
# the one that reconciler converges to, so it comes first.
#
# Run from repo root:
#     bash install/tests/test_install_kratos_panel_hostname.sh
set -uo pipefail

INSTALL_SH="${INSTALL_SH:-install.sh}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn_src=$(awk '/^_kratos_panel_hostname\(\) \{$/,/^\}$/' "$INSTALL_SH")
if [[ -z "$fn_src" ]]; then
  echo "FAIL: _kratos_panel_hostname not defined in $INSTALL_SH"
  echo "RESULT: FAIL"
  exit 1
fi

kratos_src=$(awk '/^install_kratos\(\) \{$/,/^\}$/' "$INSTALL_SH")
if ! grep -q 'panel_hostname="$(_kratos_panel_hostname "$panel_cfg")"' <<<"$kratos_src"; then
  echo "FAIL: install_kratos does not take its hostname from _kratos_panel_hostname"
  fail=1
fi

cfg="$tmp/config.toml"
printf '[server]\naddr        = "unix:/run/x.sock"\nhostname    = "old.example.com"\n' >"$cfg"

# check <name> <want> <env JABALI_SRV_HOSTNAME> <db hostname> <config path>
check() {
  local name="$1" want="$2" env_host="$3" db_host="$4" config="$5" got
  printf 'set -e\nDB_HOST=%q\n' "$db_host" >"$tmp/run.sh"
  cat >>"$tmp/run.sh" <<'EOF'
# Like the real one: sets each JABALI_SRV_* var only when unset, and exports.
reconstruct_server_env_from_db() {
  [[ -z "$DB_HOST" ]] && return 0
  : "${JABALI_SRV_HOSTNAME:=$DB_HOST}"
  export JABALI_SRV_HOSTNAME
}
hostname() { [[ "${1:-}" == "-f" ]] && echo "os.example.com"; }
EOF
  printf '%s\n' "$fn_src" >>"$tmp/run.sh"
  printf '_kratos_panel_hostname %q\n' "$config" >>"$tmp/run.sh"
  if [[ -n "$env_host" ]]; then
    got=$(JABALI_SRV_HOSTNAME="$env_host" bash "$tmp/run.sh" 2>&1)
  else
    got=$(env -u JABALI_SRV_HOSTNAME bash "$tmp/run.sh" 2>&1)
  fi
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $name: got $(printf '%q' "$got"), want $want"
    fail=1
  fi
}

check "the server_settings row wins over a stale config.toml" \
  new.example.com "" new.example.com "$cfg"
check "an exported JABALI_SRV_HOSTNAME (the installer's prompt) wins" \
  install.example.com install.example.com new.example.com "$cfg"
check "no row: config.toml" \
  old.example.com "" "" "$cfg"
check "a row value that is not a hostname is ignored" \
  old.example.com "" 'evil|s/x/y/' "$cfg"
check "no row, no config.toml: hostname -f" \
  os.example.com "" "" ""

if (( fail )); then
  echo "RESULT: FAIL"
  exit 1
fi
echo "RESULT: PASS"

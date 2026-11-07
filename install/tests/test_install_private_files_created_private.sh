#!/usr/bin/env bash
# Secret files the installer creates are private from their first byte.
#
# write_env_file (JWT_SECRET), install_sso_key (the SSO envelope key) and the
# kratos.yml render each created the file under whatever umask the shell had
# and chmod'ed it afterwards, so under a 022 umask the file was readable by
# every local account until the chmod ran. Each now creates it under umask 077.
#
# Run from repo root:
#     bash install/tests/test_install_private_files_created_private.sh
set -uo pipefail

INSTALL_SH="${INSTALL_SH:-install.sh}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# check_created_private <fn> <file> [sed expr for the fn source]
# Runs <fn> under umask 000 with chmod/chown stubbed to record <file>'s mode
# the first time either touches it: the mode the file was created with.
check_created_private() {
  local fn="$1" file="$2" sub="${3:-}" src got
  src=$(awk -v f="$fn() {" '$0 == f, /^\}$/' "$INSTALL_SH")
  if [[ -z "$src" ]]; then
    echo "FAIL: $fn not defined in $INSTALL_SH"
    fail=1
    return
  fi
  [[ -n "$sub" ]] && src=$(sed "$sub" <<<"$src")
  : >"$tmp/modes"
  {
    printf 'set -e\numask 000\n'
    printf 'F=%q\nMODES=%q\n' "$file" "$tmp/modes"
    printf 'ENV_FILE=%q\nSERVICE_USER=jabali\nSERVICE_NAME=jabali-panel\nPANEL_ADDR=unix:/run/x.sock\n' "$tmp/env/panel.env"
    cat <<'EOF'
_ok() { :; }
_log() { :; }
record() { [[ "${*: -1}" == "$F" && ! -s "$MODES" ]] && stat -c %a "$F" >"$MODES"; return 0; }
chmod() { record "$@"; command chmod "$@"; }
chown() { record "$@"; }
EOF
    printf '%s\n%s\n' "$src" "$fn"
  } >"$tmp/run.sh"
  mkdir -p "$tmp/env"
  if ! bash "$tmp/run.sh" >"$tmp/out" 2>&1; then
    echo "FAIL: $fn did not run: $(cat "$tmp/out")"
    fail=1
    return
  fi
  got=$(cat "$tmp/modes")
  if [[ ! "$got" =~ ^[0-7]00$ ]]; then
    echo "FAIL: $fn created $(basename "$file") with mode ${got:-?}, want no group/other bits"
    fail=1
  fi
}

check_created_private write_env_file "$tmp/env/panel.env"
check_created_private install_sso_key "$tmp/etc/sso.key" "s|/etc/jabali-panel|$tmp/etc|g"

# install_kratos needs a database and systemd, so its render is checked in the
# source: the redirect into kratos.yml sits inside a `( umask 077` subshell.
kratos_src=$(awk '/^install_kratos\(\) \{$/,/^\}$/' "$INSTALL_SH")
if ! awk '/^[[:space:]]*\( umask 077$/ { in_sub = 1; next }
          in_sub && /^[[:space:]]*\)$/ { in_sub = 0; next }
          in_sub && /> "\$kratos_config"$/ { found = 1 }
          END { exit !found }' <<<"$kratos_src"; then
  echo "FAIL: install_kratos does not render kratos.yml under umask 077"
  fail=1
fi

if (( fail )); then
  echo "RESULT: FAIL"
  exit 1
fi
echo "RESULT: PASS"

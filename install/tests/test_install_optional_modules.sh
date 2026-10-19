#!/usr/bin/env bash
# install/tests/test_install_optional_modules.sh — the installer's Docker,
# Docker-apps-for-users and PostgreSQL choices install what they say, and a
# headless JABALI_MODULES gets the same Requires rules as the TUI.
#
# Before this, ticking Docker, Docker apps or PostgreSQL in the installer (or
# picking the Full profile) installed nothing: install.sh had no step for those
# keys. Each choice now does what its panel switch does:
#   docker      → install_docker_engine, docker enabled + started, then
#                 docker_marketplace_enabled=1 (Server Settings → Apps);
#   docker_apps → `jabali docker enable-tenant --yes`, then
#                 docker_apps_for_users_enabled=1;
#   postgres    → install_postgres, postgresql enabled + started, then
#                 postgres_enabled=1 (Server Settings → Databases).
# A failed step warns and leaves its switch off, to turn on from the panel; the
# rest of the install goes on. And mail needs DNS (GH #2056), so a headless
# JABALI_MODULES=mail,… gets dns added, as the TUI's Resolve does.
#
# Behavioural for the new functions (extracted from install.sh and run against
# stubs), source-level for main()'s wiring.
#
# Run from repo root:
#     bash install/tests/test_install_optional_modules.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fn() { awk -v n="$1" '$0 == n "() {" {p=1} p {print} p && /^}$/ {exit}' install.sh; }
need() {
  local src
  src=$(fn "$1")
  if [[ -z "$src" ]]; then
    echo "FAIL: $1 not defined in install.sh"
    fail=1
    return 1
  fi
  eval "$src"
}

_log() { :; }
_ok() { :; }
_warn() { printf '%s\n' "$*" >>"$tmp/warn.log"; }
need is_module_enabled

# --- apply_module_requirements: the TUI's Requires rules for a headless run ---
eval "$(grep -E '^MODULE_REQUIRES=\(' install.sh)"
if need apply_module_requirements; then
  want_has() { # want_has DESC MODULES KEY
    JABALI_MODULES="$2"
    apply_module_requirements
    case ",${JABALI_MODULES}," in
      *",$3,"*) ;;
      *) echo "FAIL: $1: JABALI_MODULES='$2' → '${JABALI_MODULES}', missing '$3'"; fail=1 ;;
    esac
  }
  want_has "mail needs dns" "mail,security" dns
  want_has "docker apps need docker" "docker_apps" docker
  want_has "mail keeps itself" "mail" mail

  JABALI_MODULES="dns,mail,quota"
  apply_module_requirements
  [[ "$JABALI_MODULES" == "dns,mail,quota" ]] || { echo "FAIL: satisfied selection changed to '${JABALI_MODULES}'"; fail=1; }

  JABALI_MODULES=""
  apply_module_requirements
  [[ -z "$JABALI_MODULES" ]] || { echo "FAIL: minimal (empty) selection changed to '${JABALI_MODULES}'"; fail=1; }

  unset JABALI_MODULES
  apply_module_requirements
  [[ -z "${JABALI_MODULES+x}" ]] || { echo "FAIL: unset JABALI_MODULES became '${JABALI_MODULES}'"; fail=1; }
fi

# --- run_install_step: a failing step comes back as a status, not the end of
# the install, and it stops at its first failing command ---
if need run_install_step; then
  repo="$tmp/repo"
  mkdir -p "$repo" "$tmp/mark"
  # No ERR trap in this stand-in: its set -e alone has to stop step_fail.
  cat >"$repo/install.sh" <<'EOF'
set -Eeuo pipefail
step_ok() { : >"$MARK/ok"; }
step_fail() { false; : >"$MARK/after_fail"; }
if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then : >"$MARK/main"; fi
EOF
  out=$(MARK="$tmp/mark" REPO_DIR="$repo" bash -c '
    set -Eeuo pipefail
    trap "echo PARENT-DIED; exit 9" ERR
    eval "$1"
    if run_install_step step_fail; then echo FAIL-REPORTED-OK; fi
    run_install_step step_ok && echo OK-REPORTED-OK
    echo PARENT-GOES-ON
  ' _ "$(fn run_install_step)" 2>&1)
  grep -q PARENT-GOES-ON <<<"$out" || { echo "FAIL: a failing step ended the install: $out"; fail=1; }
  grep -q FAIL-REPORTED-OK <<<"$out" && { echo "FAIL: a failing step reported success"; fail=1; }
  grep -q OK-REPORTED-OK <<<"$out" || { echo "FAIL: a passing step reported failure: $out"; fail=1; }
  [[ -e "$tmp/mark/after_fail" ]] && { echo "FAIL: the step ran on past its failing command (set -e off)"; fail=1; }
  [[ -e "$tmp/mark/ok" ]] || { echo "FAIL: the passing step didn't run"; fail=1; }
  [[ -e "$tmp/mark/main" ]] && { echo "FAIL: sourcing install.sh for a step ran its main"; fail=1; }
fi

# --- the module steps, against stubs ---
bin="$tmp/bin"
mkdir -p "$bin"
for c in systemctl mariadb jabali; do
  cat >"$bin/$c" <<EOF
#!/bin/sh
echo "\$*" >>"$tmp/$c.log"
rc_var="STUB_RC_$c"
eval "exit \\\${\$rc_var:-0}"
EOF
  chmod 0755 "$bin/$c"
done
run_install_step() { echo "$1" >>"$tmp/steps.log"; return "${STUB_STEP_RC:-0}"; }

reset() { rm -f "$tmp"/*.log; unset STUB_STEP_RC STUB_RC_systemctl STUB_RC_mariadb STUB_RC_jabali; }
logged() { [[ -f "$tmp/$1.log" ]] && grep -qF -- "$2" "$tmp/$1.log"; }
check_module() { # check_module FN STEP UNIT FLAG
  local fnname="$1" step="$2" unit="$3" flag="$4" rc
  reset
  PATH="$bin:$PATH" "$fnname"; rc=$?
  [[ "$rc" -eq 0 ]] || { echo "FAIL: $fnname returned $rc"; fail=1; }
  logged steps "$step" || { echo "FAIL: $fnname didn't run $step"; fail=1; }
  logged systemctl "enable --now $unit" || { echo "FAIL: $fnname didn't enable and start $unit"; fail=1; }
  logged mariadb "UPDATE server_settings SET $flag=1 WHERE id=1;" || { echo "FAIL: $fnname didn't turn on $flag"; fail=1; }

  reset
  STUB_STEP_RC=1 PATH="$bin:$PATH" "$fnname"; rc=$?
  [[ "$rc" -eq 0 ]] || { echo "FAIL: $fnname returned $rc when $step failed — the install would stop"; fail=1; }
  logged mariadb "$flag" && { echo "FAIL: $fnname turned on $flag although $step failed"; fail=1; }
  [[ -s "$tmp/warn.log" ]] || { echo "FAIL: $fnname failed silently"; fail=1; }

  reset
  STUB_RC_systemctl=1 PATH="$bin:$PATH" "$fnname"; rc=$?
  [[ "$rc" -eq 0 ]] || { echo "FAIL: $fnname returned $rc when $unit didn't start"; fail=1; }
  logged mariadb "$flag" && { echo "FAIL: $fnname turned on $flag although $unit didn't start"; fail=1; }
}
need install_docker_module && check_module install_docker_module install_docker_engine docker docker_marketplace_enabled
need install_postgres_module && check_module install_postgres_module install_postgres postgresql postgres_enabled

if need install_docker_apps_module; then
  reset
  PATH="$bin:$PATH" install_docker_apps_module; rc=$?
  [[ "$rc" -eq 0 ]] || { echo "FAIL: install_docker_apps_module returned $rc"; fail=1; }
  logged jabali "docker enable-tenant --yes" || { echo "FAIL: docker apps for users didn't run 'jabali docker enable-tenant --yes'"; fail=1; }
  logged mariadb "UPDATE server_settings SET docker_apps_for_users_enabled=1 WHERE id=1;" || { echo "FAIL: docker apps for users didn't turn on docker_apps_for_users_enabled"; fail=1; }

  reset
  STUB_RC_jabali=1 PATH="$bin:$PATH" install_docker_apps_module; rc=$?
  [[ "$rc" -eq 0 ]] || { echo "FAIL: install_docker_apps_module returned $rc when enable-tenant failed — the install would stop"; fail=1; }
  logged mariadb docker_apps_for_users_enabled && { echo "FAIL: docker_apps_for_users_enabled turned on although enable-tenant failed"; fail=1; }
  [[ -s "$tmp/warn.log" ]] || { echo "FAIL: a failed enable-tenant was silent"; fail=1; }

  reset
  STUB_RC_systemctl=3 PATH="$bin:$PATH" install_docker_apps_module; rc=$?
  [[ "$rc" -eq 0 ]] || { echo "FAIL: install_docker_apps_module returned $rc without Docker"; fail=1; }
  logged jabali enable-tenant && { echo "FAIL: enable-tenant ran although Docker isn't running"; fail=1; }
  logged mariadb docker_apps_for_users_enabled && { echo "FAIL: docker_apps_for_users_enabled turned on without Docker"; fail=1; }
fi

# --- main() wiring ---
main_src=$(fn main)
line_of() { grep -nE "$1" <<<"$main_src" | head -1 | cut -d: -f1; }
req=$(line_of '^[[:space:]]*apply_module_requirements$')
def=$(line_of '^[[:space:]]*apply_default_modules$')
plan=$(line_of '^[[:space:]]*print_module_plan$')
if [[ -z "$req" ]]; then
  echo "FAIL: main() never calls apply_module_requirements"
  fail=1
elif [[ -z "$def" || -z "$plan" ]] || (( req < def || req > plan )); then
  echo "FAIL: main() must call apply_module_requirements after apply_default_modules and before the --dry-run plan"
  fail=1
fi
seed=$(line_of '^[[:space:]]*seed_module_flags$')
for pair in "docker:install_docker_module" "docker_apps:install_docker_apps_module" "postgres:install_postgres_module"; do
  key="${pair%%:*}" f="${pair##*:}"
  ln=$(line_of "^[[:space:]]*run_if_module ${key} ${f}$")
  if [[ -z "$ln" ]]; then
    echo "FAIL: main() never runs 'run_if_module ${key} ${f}' — ticking ${key} installs nothing"
    fail=1
  elif [[ -n "$seed" ]] && (( ln < seed )); then
    echo "FAIL: ${f} runs before seed_module_flags — the server_settings row may not exist yet"
    fail=1
  fi
done
d=$(line_of '^[[:space:]]*run_if_module docker install_docker_module$')
da=$(line_of '^[[:space:]]*run_if_module docker_apps install_docker_apps_module$')
if [[ -n "$d" && -n "$da" ]] && (( da < d )); then
  echo "FAIL: docker apps for users run before Docker is installed"
  fail=1
fi

# --- Python apps: main() installs the runtime, so the panel's switch
# (python_apps_enabled, default off) must follow the choice, or the feature
# stays hidden after the installer said it was installed ---
grep -qF '"python_apps_enabled:python_apps"' <<<"$(fn seed_module_flags)" \
  || { echo "FAIL: seed_module_flags doesn't seed python_apps_enabled — choosing Python apps leaves them off in the panel"; fail=1; }

# --- the --dry-run plan names every module the installer offers ---
plan_src=$(fn print_module_plan)
for key in docker docker_apps postgres; do
  grep -qE "\"${key}:" <<<"$plan_src" || { echo "FAIL: --dry-run plan doesn't list ${key}"; fail=1; }
done

# --- no ClamAV in the security module's labels: install_malware_stack
# removed it (LMD 2.0 + YARA-X) ---
for f in install.sh installer/internal/modules/registry.go panel-ui/src/shells/admin/settings/ModulesCard.tsx; do
  if grep -n 'malware/ClamAV' "$f"; then
    echo "FAIL: $f still labels the security module with ClamAV"
    fail=1
  fi
done

if [[ "$fail" -ne 0 ]]; then
  echo "RESULT: FAIL"
  exit 1
fi
echo "PASS: the installer's Docker, Docker-apps and PostgreSQL choices install, and headless modules get their requirements"

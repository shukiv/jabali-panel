#!/usr/bin/env bash
# The one-time bootstrap passwords leave panel.env once their accounts exist.
#
# seed_admin_env (and bootstrap_tenant_env, when a tenant was asked for) put a
# generated password in panel.env for the panel's first boot, which creates the
# account. No later boot reads it, so scrub_bootstrap_passwords removes it: the
# admin password once an admin exists, the tenant password once a user with the
# tenant email exists. It runs at the end of a fresh install (after the banner
# has shown the password) and on every update. The email lines stay.
#
# Run from repo root:
#     bash install/tests/test_install_bootstrap_passwords_scrubbed.sh
set -uo pipefail

INSTALL_SH="${INSTALL_SH:-install.sh}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

src=""
for fn in _bootstrap_accounts_present scrub_bootstrap_passwords; do
  body=$(awk -v f="$fn() {" '$0 == f, /^\}$/' "$INSTALL_SH")
  if [[ -z "$body" ]]; then
    echo "FAIL: $fn not defined in $INSTALL_SH"
    exit 1
  fi
  src+="$body"$'\n'
done

# A fake `jabali` CLI: `jabali --json user list` prints $tmp/users.json, or
# fails when $tmp/cli-fails exists. Every call is counted in $tmp/calls.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/jabali" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$tmp/calls"
[[ -e "$tmp/cli-fails" ]] && exit 1
[[ "\$*" == "--json user list" ]] || exit 2
cat "$tmp/users.json"
EOF
chmod 0755 "$tmp/bin/jabali"

ENV_BODY='JWT_SECRET=jwt-placeholder
DATABASE_URL=dsn-placeholder

# Admin bootstrap (consumed once on first boot, safe to leave).
JABALI_BOOTSTRAP_ADMIN_EMAIL=admin@host.example
JABALI_BOOTSTRAP_ADMIN_PASSWORD=admin-pw-placeholder

# Tenant bootstrap (GH#120 — consumed once on first boot, safe to leave).
JABALI_BOOTSTRAP_TENANT_EMAIL=Tenant@Example.test
JABALI_BOOTSTRAP_TENANT_PASSWORD=tenant-pw-placeholder
JABALI_BOOTSTRAP_TENANT_DOMAIN=example.test'

# run_scrub <users-json|FAIL> [env body]: fresh panel.env (mode 0640), then
# scrub_bootstrap_passwords under the installer's shell options.
run_scrub() {
  rm -f "$tmp/calls" "$tmp/cli-fails"
  if [[ "$1" == FAIL ]]; then : >"$tmp/cli-fails"; else printf '%s' "$1" >"$tmp/users.json"; fi
  printf '%s\n' "${2:-$ENV_BODY}" >"$tmp/panel.env"
  chmod 0640 "$tmp/panel.env"
  cp "$tmp/panel.env" "$tmp/panel.env.before"
  {
    # install.sh's own ERR trap aborts the install; a stand-in records a fire.
    printf 'set -Eeuo pipefail\ntrap "echo err-trap-fired" ERR\n'
    printf 'ENV_FILE=%q\nPATH=%q\n' "$tmp/panel.env" "$tmp/bin:$PATH"
    printf '_ok() { echo "ok: $*"; }\n_log() { :; }\n_warn() { :; }\n'
    printf '%s\n' "$src"
    printf 'scrub_bootstrap_passwords\necho scrub-returned\n'
  } >"$tmp/run.sh"
  bash "$tmp/run.sh" >"$tmp/out" 2>&1
}

has() { grep -q "^$1" "$tmp/panel.env"; }
expect() { # expect <desc> <cond...>
  local desc="$1"; shift
  if ! "$@"; then echo "FAIL: $desc"; fail=1; fi
}
returned() { grep -q '^scrub-returned$' "$tmp/out" && ! grep -q 'err-trap-fired' "$tmp/out"; }
unchanged() { cmp -s "$tmp/panel.env" "$tmp/panel.env.before"; }
mode_kept() { [[ "$(stat -c %a "$tmp/panel.env")" == 640 ]]; }
no_temp_left() { [[ -z "$(find "$tmp" -maxdepth 1 -name '.panel.env.*' -print -quit)" ]]; }

ADMIN_ONLY='{"total":2,"users":[{"id":"1","email":"admin@host.example","username":"admin","is_admin":true},{"id":"2","email":"other@example.test","username":"other","is_admin":false}]}'
BOTH='{"total":2,"users":[{"id":"1","email":"renamed@host.example","username":"admin","is_admin":true},{"id":"2","email":"tenant@example.test","username":"tenant","is_admin":false}]}'
NONE='{"total":0,"users":[]}'

# 1. An admin exists, the tenant does not: only the admin password goes.
run_scrub "$ADMIN_ONLY"
expect "admin exists: scrub returned" returned
expect "admin exists: admin password removed" eval '! has JABALI_BOOTSTRAP_ADMIN_PASSWORD='
expect "admin exists: tenant password kept (no such user yet)" has JABALI_BOOTSTRAP_TENANT_PASSWORD=
expect "admin exists: admin email kept" has JABALI_BOOTSTRAP_ADMIN_EMAIL=admin@host.example
expect "admin exists: other settings kept" has DATABASE_URL=dsn-placeholder
expect "admin exists: mode kept" mode_kept
expect "admin exists: no temp file left" no_temp_left
expect "admin exists: says how to set a new password" grep -q 'jabali user password' "$tmp/out"
expect "admin exists: only the password line removed" \
  eval '[[ "$(diff "$tmp/panel.env.before" "$tmp/panel.env" | grep -c "^[<>]")" == 1 ]]'

# 2. Both exist (the admin's email was changed since; the tenant email differs
#    in case): both passwords go.
run_scrub "$BOTH"
expect "both exist: scrub returned" returned
expect "both exist: admin password removed" eval '! has JABALI_BOOTSTRAP_ADMIN_PASSWORD='
expect "both exist: tenant password removed" eval '! has JABALI_BOOTSTRAP_TENANT_PASSWORD='
expect "both exist: tenant email and domain kept" eval 'has JABALI_BOOTSTRAP_TENANT_EMAIL= && has JABALI_BOOTSTRAP_TENANT_DOMAIN='
expect "both exist: mode kept" mode_kept

# 3. No accounts yet (first boot has not created them): nothing changes.
run_scrub "$NONE"
expect "no accounts: scrub returned" returned
expect "no accounts: panel.env unchanged" unchanged

# 4. The CLI cannot list users: nothing changes, and the install goes on.
run_scrub FAIL
expect "CLI fails: scrub returned" returned
expect "CLI fails: panel.env unchanged" unchanged

# 5. Unparseable CLI output: nothing changes, and the install goes on.
run_scrub 'not json'
expect "bad output: scrub returned" returned
expect "bad output: panel.env unchanged" unchanged

# 6. No password lines left (already scrubbed): the CLI is not even asked.
run_scrub "$ADMIN_ONLY" "$(grep -v '_PASSWORD=' <<<"$ENV_BODY")"
expect "already scrubbed: scrub returned" returned
expect "already scrubbed: panel.env unchanged" unchanged
expect "already scrubbed: CLI not called" eval '[[ ! -e "$tmp/calls" ]]'

# 7. Wiring: a fresh install scrubs after the banner showed the password, and
#    every update scrubs too.
main_src=$(awk '$0 == "main() {", /^\}$/' "$INSTALL_SH")
banner_line=$(grep -n 'Password:  %s' <<<"$main_src" | head -1 | cut -d: -f1)
scrub_line=$(grep -n '^ *scrub_bootstrap_passwords\b' <<<"$main_src" | head -1 | cut -d: -f1)
if [[ -z "$scrub_line" || -z "$banner_line" || "$scrub_line" -le "$banner_line" ]]; then
  echo "FAIL: main must call scrub_bootstrap_passwords after the banner shows the password"
  fail=1
fi
if ! awk '$0 == "provision_new_software() {", /^\}$/' "$INSTALL_SH" | grep -q '^ *scrub_bootstrap_passwords\b'; then
  echo "FAIL: provision_new_software must call scrub_bootstrap_passwords"
  fail=1
fi

if [[ $fail -eq 0 ]]; then
  echo "PASS: bootstrap passwords leave panel.env once their accounts exist"
fi
exit $fail

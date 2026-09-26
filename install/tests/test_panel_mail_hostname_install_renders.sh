#!/usr/bin/env bash
# install/tests/test_panel_mail_hostname_install_renders.sh — install.sh
# renders the panel mail hostname from the DB, not from mail.<hostname>.
#
# JAB-390 lets the shared panel mail hostname be any FQDN; the reconciler's
# switchover writes the applied name to server_settings.mail_hostname. The
# install-rendered consumers — Bulwark's JMAP_SERVER_URL in bulwark.env and
# the /webmail redirects in the default nginx vhost — derived
# mail.<hostname> on every render, so a `jabali update` that re-renders them
# after a switchover would silently revert them. Bulwark's JMAP URL must
# also match the per-domain webmail vhost's sub_filter, or tenant webmail
# breaks cross-origin.
#
# install.sh now resolves the name through _panel_mail_hostname, which reads
# `jabali settings mail-hostname --applied` and falls back to mail.<host>.
# The value lands in nginx config and bulwark.env, so anything that is not a
# bare lower-case FQDN is refused (warned, derived name used).
#
# Behavioural: extracts the functions from install.sh and runs them against
# a stub CLI and temp paths.
#
# Run from repo root:
#     bash install/tests/test_panel_mail_hostname_install_renders.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# extract FN — the body of FN from install.sh. A heredoc inside the body may
# hold a `}` at column 0 (nginx config), so the closing brace only counts
# outside one.
extract() {
  awk -v fn="$1" '
    $0 ~ "^" fn "\\(\\) \\{$" {p = 1}
    !p {next}
    {print}
    hd != "" { if ($0 == hd) hd = ""; next }
    match($0, /<<-? *[\047"]?[A-Za-z_]+[\047"]?/) {
      hd = substr($0, RSTART, RLENGTH); gsub(/^<<-? *|[\047"]/, "", hd); next
    }
    /^\}$/ {exit}
  ' install.sh
}

resolver_src=$(extract _panel_mail_hostname)
if [[ -z "$resolver_src" ]]; then
  echo "FAIL: _panel_mail_hostname() not found in install.sh"
  exit 1
fi
eval "$resolver_src"

warned=""
_warn() { warned="$*"; }

# The stub CLI records its arguments, prints $STUB_OUT, exits $STUB_RC.
BIN_PATH="$tmp/jabali"
cat >"$BIN_PATH" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$STUB_ARGS"
printf '%b' "$STUB_OUT"
exit "$STUB_RC"
STUB
chmod +x "$BIN_PATH"
export STUB_ARGS="$tmp/args"

# resolve DESC STUB_OUT STUB_RC WANT WANT_WARN(yes|no)
resolve() {
  local got
  warned=""
  STUB_OUT="$2" STUB_RC="$3" _panel_mail_hostname panel.example.com >"$tmp/out"
  got=$(cat "$tmp/out")
  if [[ "$got" != "$4" ]]; then
    echo "FAIL: $1: got '$got', want '$4'"
    fail=1
  fi
  if [[ "$5" == yes && -z "$warned" ]]; then
    echo "FAIL: $1: a refused value must be warned about"
    fail=1
  fi
  if [[ "$5" == no && -n "$warned" ]]; then
    echo "FAIL: $1: unexpected warning: $warned"
    fail=1
  fi
}

resolve "applied custom name"          'mx.example.net\n' 0 mx.example.net      no
resolve "no applied name"              ''                 0 mail.panel.example.com no
resolve "CLI fails (fresh install)"    ''                 1 mail.panel.example.com no
resolve "config injection refused"     'mx.example.net; return 301 https://evil\n' 0 mail.panel.example.com yes
resolve "URL refused"                  'https://mx.example.net/\n' 0 mail.panel.example.com yes
resolve "single label refused"         'localhost\n'      0 mail.panel.example.com yes
resolve "only the first line is read"  'mx.example.net\nserver { }\n' 0 mx.example.net no
if [[ "$(cat "$STUB_ARGS")" != "settings mail-hostname --applied" ]]; then
  echo "FAIL: the resolver must call 'settings mail-hostname --applied', called '$(cat "$STUB_ARGS")'"
  fail=1
fi
warned=""
got=$(BIN_PATH="$tmp/missing" _panel_mail_hostname panel.example.com)
if [[ "$got" != mail.panel.example.com ]]; then
  echo "FAIL: no CLI installed: got '$got', want mail.panel.example.com"
  fail=1
fi

# --- bulwark.env: rendered through the resolver ---
env_src=$(extract _install_bulwark_env)
if [[ -z "$env_src" ]]; then
  echo "FAIL: _install_bulwark_env() not found in install.sh"
  exit 1
fi
# Point the renderer at a temp destination; stub the privileged calls.
eval "${env_src//\/etc\/jabali-panel\/bulwark.env/$tmp/bulwark.env}"
install() { cp "${@: -2:1}" "${@: -1}"; }
systemctl() { return 1; }
_ok() { :; }
_die() { echo "FAIL: _install_bulwark_env died: $*"; exit 1; }
REPO_DIR="$PWD"

render_env() { # render_env STUB_OUT -> prints JMAP_SERVER_URL and LOGIN_COMPANY_NAME
  rm -f "$tmp/bulwark.env"
  JABALI_SRV_HOSTNAME=panel.example.com STUB_OUT="$1" STUB_RC=0 _install_bulwark_env >/dev/null
  grep -E '^(JMAP_SERVER_URL|LOGIN_COMPANY_NAME)=' "$tmp/bulwark.env"
}
want_custom=$'JMAP_SERVER_URL=https://mx.example.net\nLOGIN_COMPANY_NAME=panel.example.com'
want_derived=$'JMAP_SERVER_URL=https://mail.panel.example.com\nLOGIN_COMPANY_NAME=panel.example.com'
if [[ "$(render_env 'mx.example.net\n')" != "$want_custom" ]]; then
  echo "FAIL: bulwark.env with an applied mail hostname:"
  render_env 'mx.example.net\n' | sed 's/^/    /'
  fail=1
fi
if [[ "$(render_env '')" != "$want_derived" ]]; then
  echo "FAIL: bulwark.env without an applied mail hostname:"
  render_env '' | sed 's/^/    /'
  fail=1
fi
if grep -q '\${JABALI_' "$tmp/bulwark.env"; then
  echo "FAIL: bulwark.env keeps an unrendered template variable: $(grep -o '\${JABALI_[A-Z_]*}' "$tmp/bulwark.env" | head -1)"
  fail=1
fi

# --- default nginx vhost: /webmail redirects go through the resolver ---
vhost_src=$(extract install_nginx_default_vhost)
if grep -q 'https://mail\.\${JABALI_SRV_HOSTNAME}' <<<"$vhost_src"; then
  echo "FAIL: install_nginx_default_vhost still redirects /webmail to a derived mail.\${JABALI_SRV_HOSTNAME}"
  fail=1
fi
if ! grep -q '_panel_mail_hostname "\$JABALI_SRV_HOSTNAME"' <<<"$vhost_src"; then
  echo "FAIL: install_nginx_default_vhost does not resolve the mail hostname through _panel_mail_hostname"
  fail=1
fi
redirects=$(grep -c 'return 301 https://\${_mail_host}/;' <<<"$vhost_src" || true)
if [[ "$redirects" -ne 4 ]]; then
  echo "FAIL: install_nginx_default_vhost has $redirects /webmail redirects to \${_mail_host}, want 4"
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo "OK: install.sh renders the applied panel mail hostname (bulwark.env, /webmail redirects) and refuses anything but a bare FQDN"
else
  exit 1
fi

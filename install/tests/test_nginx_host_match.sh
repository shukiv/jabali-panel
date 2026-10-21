#!/usr/bin/env bash
# install/tests/test_nginx_host_match.sh — nginx refuses a request whose Host
# header names a different host from the one it serves the request for.
#
# With an absolute request target ("GET http://a.example/ HTTP/1.1") nginx
# routes by the target's host and ignores the Host header, while the CrowdSec
# bouncer and the applications read the Host header.
# install/nginx/jabali-host-match.conf answers such a request with 400.
#
# 1. Static: the snippet runs in the rewrite phase (the bouncer owns the
#    http-level access_by_lua_block, and a second one fails nginx -t),
#    normalises the Host header the way nginx derives $host, and exits 400.
# 2. Functional: ensure_nginx_host_match, with install/nginx/systemctl
#    stubbed, against temp files: installs only where the bouncer's nginx conf
#    is, removes it where that is gone, idempotent, puts the old file back
#    when nginx -t fails, skips when the source is missing.
# 3. Static: both the bouncer install and `jabali update`
#    (provision_new_software) call it; uninstall's conf.d glob removes it.
#
# Run from repo root:
#     bash install/tests/test_nginx_host_match.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

fail=0
conf=install/nginx/jabali-host-match.conf

# --- 1. The snippet. ---
if [[ ! -f "$conf" ]]; then
  echo "FAIL: $conf missing"
  exit 1
fi
if ! grep -qE '^rewrite_by_lua_block \{$' "$conf"; then
  echo "FAIL: $conf must be an http-level rewrite_by_lua_block (it runs before the bouncer's access phase)"
  fail=1
fi
if grep -qE '^[[:space:]]*access_by_lua' "$conf"; then
  echo "FAIL: $conf must not use access_by_lua*: crowdsec_nginx.conf already sets it at http level"
  fail=1
fi
for want in \
  'if ngx.is_subrequest then' \
  'local hdr = ngx.var.http_host' \
  'if hdr == nil or hdr == "" then' \
  'local h = string.lower(hdr)' \
  'if string.sub(h, 1, 1) == "[" then' \
  'local e = string.find(h, "]", 1, true)' \
  'local c = string.find(h, ":", 1, true)' \
  'h = string.sub(h, 1, c - 1)' \
  'if string.sub(h, -1) == "." then' \
  'if h ~= ngx.var.host then' \
  'return ngx.exit(ngx.HTTP_BAD_REQUEST)'; do
  if ! grep -qF -- "$want" "$conf"; then
    echo "FAIL: $conf missing: $want"
    fail=1
  fi
done

# --- 2. The converger, exercised against temp files. ---
fn_src=$(awk '/^ensure_nginx_host_match\(\) \{$/,/^\}$/' install.sh)
if [[ -z "$fn_src" ]]; then
  echo "FAIL: ensure_nginx_host_match not defined in install.sh"
  exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
dst="$tmpdir/jabali-host-match.conf"
bouncer="$tmpdir/crowdsec_nginx.conf"
calls="$tmpdir/calls"
export REPO_DIR="$tmpdir/repo"
mkdir -p "$REPO_DIR/install/nginx"
cp "$conf" "$REPO_DIR/install/nginx/jabali-host-match.conf"

_ok()   { :; }
_log()  { :; }
_warn() { :; }
# `install -m .. -o .. -g .. SRC DST`: copy the last two args (no root needed).
install() { local a=("$@"); cp "${a[-2]}" "${a[-1]}"; }
# nginx -t result is NGINX_T_RC; every reload is recorded.
nginx() { echo "nginx $*" >>"$calls"; return "${NGINX_T_RC:-0}"; }
systemctl() { echo "systemctl $*" >>"$calls"; return 0; }

fn_tmp="${fn_src//\/etc\/nginx\/conf.d\/jabali-host-match.conf/$dst}"
fn_tmp="${fn_tmp//\/etc\/nginx\/conf.d\/crowdsec_nginx.conf/$bouncer}"
run_converger() { : >"$calls"; ( eval "$fn_tmp"; ensure_nginx_host_match ); }
reloaded() { grep -q '^systemctl reload nginx' "$calls"; }

# 2a. No bouncer → nothing installed, nothing reloaded.
rm -f "$dst" "$bouncer"; NGINX_T_RC=0 run_converger
if [[ -e "$dst" ]]; then
  echo "FAIL: installed the Lua snippet on a box without the bouncer (no Lua module → nginx -t fails)"
  fail=1
fi
if reloaded; then
  echo "FAIL: reloaded nginx with nothing to do"
  fail=1
fi

# 2b. Bouncer present → installed, nginx reloaded.
touch "$bouncer"; rm -f "$dst"; NGINX_T_RC=0 run_converger
if ! cmp -s "$conf" "$dst"; then
  echo "FAIL: did not install the snippet where the bouncer is"
  fail=1
fi
if ! reloaded; then
  echo "FAIL: installed the snippet but did not reload nginx"
  fail=1
fi

# 2c. Already current → untouched, no reload.
NGINX_T_RC=0 run_converger
if ! cmp -s "$conf" "$dst" || reloaded; then
  echo "FAIL: not idempotent: a current snippet was rewritten or nginx reloaded"
  fail=1
fi

# 2d. nginx -t fails on a first install → removed, no reload.
rm -f "$dst"; NGINX_T_RC=1 run_converger
if [[ -e "$dst" ]]; then
  echo "FAIL: kept a snippet nginx -t refused (nginx left broken)"
  fail=1
fi
if reloaded; then
  echo "FAIL: reloaded nginx after nginx -t failed"
  fail=1
fi

# 2e. nginx -t fails on an update → the previous file is put back.
echo "# previous version" >"$dst"; NGINX_T_RC=1 run_converger
if [[ "$(cat "$dst" 2>/dev/null)" != "# previous version" ]]; then
  echo "FAIL: did not put the previous snippet back after nginx -t failed"
  fail=1
fi

# 2f. Changed source → replaced, reloaded.
NGINX_T_RC=0 run_converger
if ! cmp -s "$conf" "$dst" || ! reloaded; then
  echo "FAIL: did not replace an outdated snippet and reload"
  fail=1
fi

# 2g. Bouncer gone → snippet removed.
rm -f "$bouncer"; NGINX_T_RC=0 run_converger
if [[ -e "$dst" ]]; then
  echo "FAIL: kept the Lua snippet after the bouncer's nginx conf was removed"
  fail=1
fi

# 2h. Source missing → nothing created.
touch "$bouncer"; rm -f "$dst"; rm -rf "$REPO_DIR/install/nginx"; NGINX_T_RC=0 run_converger
if [[ -e "$dst" ]]; then
  echo "FAIL: created the snippet with no source file"
  fail=1
fi

# --- 3. Call sites and uninstall. ---
# Capture awk output first: `awk | grep -q` trips pipefail on SIGPIPE.
bouncer_src=$(awk '/^install_crowdsec_nginx_bouncer\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE '^[[:space:]]+ensure_nginx_host_match$' <<<"$bouncer_src"; then
  echo "FAIL: install_crowdsec_nginx_bouncer does not call ensure_nginx_host_match"
  fail=1
fi
upd_src=$(awk '/^provision_new_software\(\) \{$/,/^\}$/' install.sh)
if ! grep -qE '^[[:space:]]+ensure_nginx_host_match$' <<<"$upd_src"; then
  echo "FAIL: provision_new_software (jabali update) does not call ensure_nginx_host_match"
  fail=1
fi
if ! grep -qF 'rm -f  /etc/nginx/conf.d/jabali-*.conf' install.sh; then
  echo "FAIL: uninstall no longer removes /etc/nginx/conf.d/jabali-*.conf"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "PASS: nginx Host check installed by the bouncer install and jabali update"

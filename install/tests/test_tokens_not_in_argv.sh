#!/usr/bin/env bash
# Tokens the installer and the free-hostname scripts send never go on a
# command line, where any local account can read them in /proc/<pid>/cmdline.
#
# The heartbeat and both certbot hooks put the box's hostname token in curl's
# -d argument, install.sh's jh_post did the same with the emailed claim code,
# and install_redis_acl handed redis-cli the panel token with --pass. curl now
# reads the body from stdin, and redis-cli takes REDISCLI_AUTH.
#
# Run from repo root:
#     bash install/tests/test_tokens_not_in_argv.sh
set -uo pipefail

INSTALL_SH="${INSTALL_SH:-install.sh}"
HOSTNAME_DIR="${HOSTNAME_DIR:-install/hostname}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
secret="tok-$RANDOM$RANDOM"

# A fake curl records its argv and, for a body given as @-, its stdin. It
# answers 200 (alone when -o sends the body away, after "{}" otherwise).
mkdir -p "$tmp/bin"
cat >"$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >>"$CALLS/argv"
quiet=0
for a in "$@"; do
  [[ "$a" == "@-" ]] && cat >>"$CALLS/stdin"
  [[ "$a" == "-o" ]] && quiet=1
done
if (( quiet )); then printf '200'; else printf '{}\n200'; fi
EOF
printf '#!/bin/sh\nexit 0\n' >"$tmp/bin/sleep"
printf '#!/bin/sh\nexit 0\n' >"$tmp/bin/logger"
chmod +x "$tmp/bin/"*
printf 'LABEL=x\nFQDN=x.example.test\nTOKEN=%s\nAPI=https://api.example.test\n' "$secret" >"$tmp/hostname.env"

# check <name>: the token reached curl on stdin, and never in argv.
check() {
  local name="$1"
  if grep -qF "$secret" "$tmp/calls/argv" 2>/dev/null; then
    echo "FAIL: $name puts the token in curl's argv"
    fail=1
  fi
  if ! grep -qF "$secret" "$tmp/calls/stdin" 2>/dev/null; then
    echo "FAIL: $name does not send the token in the body on stdin"
    fail=1
  fi
}

for s in jabali-hostname-heartbeat.sh certbot-auth-hook.sh certbot-cleanup-hook.sh; do
  rm -rf "$tmp/calls"
  mkdir -p "$tmp/calls"
  sed "s|^ENV=/etc/jabali-panel/hostname.env$|ENV=$tmp/hostname.env|" "$HOSTNAME_DIR/$s" >"$tmp/$s"
  if ! grep -qx "ENV=$tmp/hostname.env" "$tmp/$s"; then
    echo "FAIL: $s no longer reads /etc/jabali-panel/hostname.env"
    fail=1
    continue
  fi
  if ! PATH="$tmp/bin:$PATH" CALLS="$tmp/calls" CERTBOT_DOMAIN=x.example.test CERTBOT_VALIDATION=v \
    bash "$tmp/$s" >"$tmp/out" 2>&1; then
    echo "FAIL: $s failed: $(cat "$tmp/out")"
    fail=1
    continue
  fi
  check "$s"
done

fn_src=$(awk '/^jh_post\(\) \{$/,/^\}$/' "$INSTALL_SH")
if [[ -z "$fn_src" ]]; then
  echo "FAIL: jh_post not defined in $INSTALL_SH"
  fail=1
else
  rm -rf "$tmp/calls"
  mkdir -p "$tmp/calls"
  printf '%s\njh_post /v1/claim %q >/dev/null\n' "$fn_src" "{\"email\":\"a@example.test\",\"code\":\"$secret\"}" >"$tmp/jh.sh"
  PATH="$tmp/bin:$PATH" CALLS="$tmp/calls" JH_API=https://api.example.test bash "$tmp/jh.sh"
  check "install.sh jh_post"
fi

# install_redis_acl needs a running Redis, so it is checked in the source.
if grep -nE 'redis-cli[^#]*(--pass|[[:space:]]-a[[:space:]])' "$INSTALL_SH"; then
  echo "FAIL: install.sh gives redis-cli a password in argv; use REDISCLI_AUTH"
  fail=1
fi

if (( fail )); then
  echo "RESULT: FAIL"
  exit 1
fi
echo "RESULT: PASS"

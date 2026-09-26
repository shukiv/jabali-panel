#!/usr/bin/env bash
# install/tests/test_stalwart_push_cert_san_replace.sh — a pushed certificate
# replaces every Stalwart registry entry that serves any of its names.
#
# jabali-stalwart-push-cert deleted prior x:Certificate entries only when
# their SAN set contained the NEW cert's CN. When the new cert's CN is a
# name the registry has not seen yet (a renamed or custom shared mail
# hostname, JAB-390: CN=mx.example.net, SAN also covers the old
# mail.<panel-hostname>), the old entry survived, the create did not
# replace it, and Stalwart kept serving the OLD certificate on :993/:465
# (observed on the .60 test box, 2026-09-26). The fix: delete every entry
# whose SAN set overlaps any name the new cert covers (CN + SANs), and
# nothing else — a tenant's per-domain mail cert must never be removed by
# a panel push.
#
# Behavioural: runs a copy of the script with stalwart-cli replaced by a
# stub that serves a fixture registry and records deletes and applies.
#
# Run from repo root:
#     bash install/tests/test_stalwart_push_cert_san_replace.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

script="install/stalwart/jabali-stalwart-push-cert.sh"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The stub CLI: `query` prints $STUB_REGISTRY, `delete --ids X` and `apply`
# append to $STUB_LOG.
cat >"$tmp/stalwart-cli" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  query)  cat "$STUB_REGISTRY" ;;
  delete) shift 2; [[ "$1" == "--ids" ]] && echo "delete $2" >>"$STUB_LOG" ;;
  apply)  cat >/dev/null; echo "apply" >>"$STUB_LOG" ;;
esac
exit 0
STUB
chmod +x "$tmp/stalwart-cli"
printf 'STALWART_RECOVERY_ADMIN=admin:test-only-not-a-secret\n' >"$tmp/stalwart.env"

# A copy of the script pointed at the stub CLI and env file. Only the two
# path assignments change.
sed -e "s|^STW_CLI=.*|STW_CLI=\"$tmp/stalwart-cli\"|" \
    -e "s|^STW_ENV=.*|STW_ENV=\"$tmp/stalwart.env\"|" \
    "$script" >"$tmp/push-cert.sh"
if ! grep -q "^STW_CLI=\"$tmp/stalwart-cli\"" "$tmp/push-cert.sh" \
  || ! grep -q "^STW_ENV=\"$tmp/stalwart.env\"" "$tmp/push-cert.sh"; then
  echo "FAIL: could not point $script at the stub (STW_CLI= / STW_ENV= lines changed?)"
  exit 1
fi

# mkcert NAME CN [SAN,...] — a self-signed cert + key in $tmp/NAME.{crt,key}
mkcert() {
  local ext=()
  if [[ -n "${3:-}" ]]; then
    ext=(-addext "subjectAltName=$3")
  fi
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
    -subj "/CN=$2" "${ext[@]}" -keyout "$tmp/$1.key" -out "$tmp/$1.crt" 2>/dev/null
}

registry='{"subjectAlternativeNames":{"mail.panel.example.com":true},"id":"panel-old"}
{"subjectAlternativeNames":{"mx.example.net":true},"id":"mx-stale"}
{"subjectAlternativeNames":{"MX.Example.NET":true},"id":"mx-upper"}
{"subjectAlternativeNames":{"autoconfig.tenant.com":true,"autodiscover.tenant.com":true,"mail.tenant.com":true},"id":"tenant"}
{"subjectAlternativeNames":{"localhost":true},"id":"rcgen"}'
printf '%s\n' "$registry" >"$tmp/registry.ndjson"

# run CERT_BASENAME — push $tmp/CERT_BASENAME.{crt,key}; prints the stub log
run() {
  : >"$tmp/log"
  STUB_REGISTRY="$tmp/registry.ndjson" STUB_LOG="$tmp/log" \
  JABALI_STALWART_CERT_PATH="$tmp/$1.crt" JABALI_STALWART_KEY_PATH="$tmp/$1.key" \
    bash "$tmp/push-cert.sh" 2>/dev/null || true
  cat "$tmp/log"
}

# expect DESC LOG WANT_DELETED... — exactly these ids deleted, apply after them
expect() {
  local desc="$1" log="$2"
  shift 2
  local got want
  got=$({ grep '^delete ' <<<"$log" || true; } | sed 's/^delete //' | sort | tr '\n' ' ')
  want=$(printf '%s\n' "$@" | sed '/^$/d' | sort | tr '\n' ' ')
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $desc: deleted [${got% }], want [${want% }]"
    fail=1
  fi
  if [[ "$(tail -n 1 <<<"$log")" != "apply" ]]; then
    echo "FAIL: $desc: the create was not applied after the deletes"
    fail=1
  fi
}

# 1. transition cert for a shared mail hostname change: CN is the NEW name,
#    SAN also covers the old one -> both old entries go, tenant + rcgen stay.
mkcert switch mx.example.net "DNS:mx.example.net,DNS:mail.panel.example.com"
expect "transition cert (new CN, old name in SAN)" "$(run switch)" panel-old mx-stale mx-upper

# 2. plain renewal of the panel mail cert -> only its own entry.
mkcert renew mail.panel.example.com "DNS:mail.panel.example.com"
expect "renewal of the panel mail cert" "$(run renew)" panel-old

# 3. cert with no SAN extension -> its CN still matches.
mkcert nosan mail.panel.example.com
expect "cert with CN only" "$(run nosan)" panel-old

# 4. per-domain tenant mail cert -> only the tenant entry, never the panel's.
mkcert tenant mail.tenant.com "DNS:mail.tenant.com,DNS:autoconfig.tenant.com,DNS:autodiscover.tenant.com"
expect "per-domain tenant cert" "$(run tenant)" tenant

# 5. names are compared case-insensitively (DNS names are) — on the cert
#    side here, on the registry side via mx-upper in case 1.
mkcert upper MAIL.PANEL.EXAMPLE.COM "DNS:MAIL.PANEL.EXAMPLE.COM"
expect "upper-case cert names" "$(run upper)" panel-old

# 6. a cert no entry serves -> nothing deleted, still applied.
mkcert fresh mail.new.example.org "DNS:mail.new.example.org"
expect "first push of a new name" "$(run fresh)"

if [[ "$fail" -eq 0 ]]; then
  echo "OK: push-cert replaces every registry entry serving any of the cert's names, and only those"
else
  exit 1
fi

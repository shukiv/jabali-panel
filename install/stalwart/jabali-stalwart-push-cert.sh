#!/usr/bin/env bash
# jabali-stalwart-push-cert.sh — push /etc/jabali/tls/panel-mail.{crt,key}
# into Stalwart as the default TLS certificate (Certificate object via
# the JMAP management API). Without this, Stalwart serves its rcgen
# self-signed cert (CN=rcgen self signed cert, SAN=localhost) on
# IMAPS/465/587 and every external mail client rejects the handshake.
#
# Idempotent: deletes any previously-pushed Certificate named
# "jabali-panel-mail" then re-creates it from the current on-disk PEM.
# Safer than update-or-create because Stalwart treats Certificate.name
# as immutable — a content drift between disk + Stalwart resolves to
# "create with new contents" rather than partial updates.
#
# Invoked by:
#   - install.sh after _install_stalwart_apply (first push)
#   - install/letsencrypt/jabali-panel-cert.sh on certbot renewal of the
#     mail.<panel-hostname> lineage (kind=mail)
#
# Reads creds from /etc/jabali-panel/stalwart.env (STALWART_RECOVERY_ADMIN
# = "admin:<password>"). Talks to the loopback JMAP listener on
# 127.0.0.1:8446. set -euo pipefail.

set -euo pipefail

# Caller can override the cert/key/name trio via env vars so the M6.6
# per-domain mail TLS path can re-use this same script for tenant
# domains. Default is the M6.4 panel-hostname mail cert.
CERT_PATH="${JABALI_STALWART_CERT_PATH:-/etc/jabali/tls/panel-mail.crt}"
KEY_PATH="${JABALI_STALWART_KEY_PATH:-/etc/jabali/tls/panel-mail.key}"
CERT_NAME="${JABALI_STALWART_CERT_NAME:-jabali-panel-mail}"
STW_URL="${STALWART_URL:-http://127.0.0.1:8446}"
STW_ENV="/etc/jabali-panel/stalwart.env"
STW_CLI="/usr/local/bin/stalwart-cli"

log() { echo "jabali-stalwart-push-cert: $*" >&2; }

if [[ ! -x "$STW_CLI" ]]; then
  log "stalwart-cli not found at $STW_CLI — skipping"
  exit 0
fi
if [[ ! -f "$CERT_PATH" || ! -f "$KEY_PATH" ]]; then
  log "$CERT_PATH or $KEY_PATH missing — nothing to push"
  exit 0
fi
if [[ ! -f "$STW_ENV" ]]; then
  log "$STW_ENV missing — cannot resolve admin creds, skipping"
  exit 0
fi
if ! command -v jq >/dev/null; then
  log "jq missing — required for JSON-escape of PEM contents, skipping"
  exit 0
fi

# Parse STALWART_RECOVERY_ADMIN=admin:<password> from the env file.
admin_line="$(grep -E '^STALWART_RECOVERY_ADMIN=' "$STW_ENV" | head -1 | cut -d= -f2-)"
if [[ -z "$admin_line" ]]; then
  log "STALWART_RECOVERY_ADMIN not set in $STW_ENV — cannot push, skipping"
  exit 0
fi
admin_user="${admin_line%%:*}"
admin_pass="${admin_line#*:}"
if [[ -z "$admin_user" || -z "$admin_pass" ]]; then
  log "STALWART_RECOVERY_ADMIN malformed in $STW_ENV — cannot push, skipping"
  exit 0
fi

# Wait briefly for Stalwart to come up. The mgmt API is the same socket
# Stalwart starts listening on after RocksDB open + Bootstrap apply.
ready=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if STALWART_URL="$STW_URL" STALWART_USER="$admin_user" STALWART_PASSWORD="$admin_pass" \
      "$STW_CLI" query x:Certificate --json >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [[ "$ready" != "1" ]]; then
  log "stalwart-cli could not reach $STW_URL after 10 retries — leaving cert un-pushed"
  exit 0
fi

# Delete any prior Certificate covering the SAME primary hostname so
# the next create carries the fresh PEM (renewals must REPLACE, not
# duplicate — two certs with the same SAN make Stalwart's SNI pick
# ambiguous, and re-creating under the same name key hits a
# primaryKeyViolation that silently keeps the OLD content).
#
# `stalwart-cli query x:Certificate --json` emits ONE JSON object per
# line (NDJSON), each shaped:
#   {"subjectAlternativeNames":{"mail.example.com":true},"id":"<opaque>"}
# The `id` is a Stalwart-assigned opaque handle, NOT the name we pass
# on create — so we can't match prior certs by CERT_NAME. Match by SAN
# instead: pull the cert's own primary CN (= the certbot primary
# domain, e.g. mail.<d>) and delete every registry entry whose SAN set
# contains it. This scopes the delete to THIS cert's hostname and
# never touches the panel cert (different SAN).
#
# Robust under `set -euo pipefail`: the old `jq '.[] | select(.id...)'`
# crashed with "Cannot index string with string" because the output is
# NDJSON objects, not a JSON array — `.[]` iterated an object's values
# (the SAN map + the id string) and `.id` on the string aborted the
# whole script before the create ever ran (GH #132: per-domain mail
# cert never reached Stalwart).
primary_san="$(openssl x509 -in "$CERT_PATH" -noout -subject 2>/dev/null \
  | sed -n 's/.*CN *= *\([^,]*\).*/\1/p' | head -1)"
if [[ -n "$primary_san" ]]; then
  mapfile -t prior_ids < <(
    STALWART_URL="$STW_URL" STALWART_USER="$admin_user" STALWART_PASSWORD="$admin_pass" \
      "$STW_CLI" query x:Certificate --json 2>/dev/null \
      | jq -r --arg san "$primary_san" \
          'select(.subjectAlternativeNames[$san] == true) | .id' 2>/dev/null || true
  )
  for prior_id in "${prior_ids[@]}"; do
    [[ -n "$prior_id" ]] || continue
    STALWART_URL="$STW_URL" STALWART_USER="$admin_user" STALWART_PASSWORD="$admin_pass" \
      "$STW_CLI" delete x:Certificate --ids "$prior_id" >/dev/null 2>&1 || true
  done
fi

# Build the create plan with JSON-escaped PEM. jq -Rs reads stdin raw +
# emits a single JSON-string literal — handles newlines, quotes, the
# whole bundle.
cert_pem_json="$(jq -Rs . <"$CERT_PATH")"
key_pem_json="$(jq -Rs . <"$KEY_PATH")"

# stalwart-cli v1.0.7 dropped JSON-array plans and now only accepts
# NDJSON (one top-level object per line). Emit the single create op
# directly on one line + feed via --stdin.
plan_file="$(mktemp -t jabali-stalwart-cert-plan.XXXXXX.ndjson)"
trap 'rm -f "$plan_file"' EXIT
# Stalwart's Certificate schema types `certificate` as object<PublicText>
# and `privateKey` as object<SecretText> — both are tagged-enum wrappers.
# A raw PEM string is rejected with
#   invalidPatch | Missing or invalid '@type' property in object
# Both variants share @type "Text" (NOT "PublicText" / "SecretText" as
# the schema label might suggest) — Stalwart distinguishes the two by
# the INNER field name:
#   PublicText: {"@type":"Text","value":"<PEM>"}
#   SecretText: {"@type":"Text","secret":"<PEM>"}   <-- note: secret, not value
# Probed empirically against stalwart-cli 1.0.7 + Stalwart 0.16.6 on
# 2026-05-24. Same scar pattern as the M6 wt-c JMAP `Manual` regression
# (feedback_schema_enumerate_kinds_not_names.md) — schema property
# KINDS matter, and inner field names are NOT uniform across
# tagged-enum variants of the same @type.
jq -nc --arg name "$CERT_NAME" --argjson cert "$cert_pem_json" --argjson key "$key_pem_json" \
  '{"@type":"create","object":"x:Certificate","value":{($name):{
      "certificate":{"@type":"Text","value":$cert},
      "privateKey":{"@type":"Text","secret":$key}
   }}}' \
  >"$plan_file"

push_out="$(STALWART_URL="$STW_URL" STALWART_USER="$admin_user" STALWART_PASSWORD="$admin_pass" \
  "$STW_CLI" apply --continue-on-error --stdin <"$plan_file" 2>&1)"
push_rc=$?
# Treat a re-push of the same certificate name as success (Certificate.name
# is the primary key; primaryKeyViolation == cert already exists). Anything
# else (auth, parse, RocksDB) is a real failure — surface it.
if (( push_rc != 0 )) && ! printf '%s\n' "$push_out" | grep -q 'primaryKeyViolation'; then
  log "stalwart-cli apply failed — Stalwart will keep serving the previous cert"
  printf '%s\n' "$push_out" | head -5 >&2
  exit 0
fi

log "pushed Stalwart Certificate '$CERT_NAME' from $CERT_PATH ($(stat -c %Y "$CERT_PATH" | xargs -I{} date -d @{} -Iseconds))"

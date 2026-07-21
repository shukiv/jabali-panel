#!/usr/bin/env bash
# Publish a built jabali release to Cloudflare R2 so the installer bootstrap can
# resolve it WITHOUT the GitHub API (which caps unauthenticated requests at
# 60/hr/IP — cloud VMs on shared-egress IPs hit that constantly, GH bootstrap
# "403 API rate limit exceeded"). R2 is S3-compatible; we sign with curl's
# native --aws-sigv4 so there is no aws-cli / rclone dependency.
#
# Uploads three objects to the bucket root:
#   jabali-release-<sha>.tar.gz          (immutable, long cache)
#   jabali-release-<sha>.tar.gz.sha256   (immutable, long cache)
#   latest.json                          (short cache — the pointer the
#                                         bootstrap reads first)
#
# Required env:
#   R2_ACCOUNT_ID           Cloudflare account id (the R2 S3 endpoint host)
#   R2_BUCKET               bucket name
#   R2_ACCESS_KEY_ID        R2 S3 access key id
#   R2_SECRET_ACCESS_KEY    R2 S3 secret
# Optional env:
#   R2_PUBLIC_BASE          public URL the objects are served at
#                           (default https://dl.jabali-panel.com)
#   DIST_DIR                where the tarball lives (default ./dist)
#   SHORT_SHA / FULL_SHA    release identity (default: git rev-parse)
#   PUBLISHED_AT            ISO-8601 timestamp (default: date -u)
set -euo pipefail

die() { printf '\033[1;31m[r2-publish] %s\033[0m\n' "$*" >&2; exit 1; }
log() { printf '\033[1;34m[r2-publish]\033[0m %s\n' "$*"; }

for v in R2_ACCOUNT_ID R2_BUCKET R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY; do
  [[ -n "${!v:-}" ]] || die "missing required env: $v"
done
command -v curl >/dev/null 2>&1 || die "curl not found"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum not found"
# curl's S3 signer landed in 7.75.0; fail early with a clear message otherwise.
curl --help all 2>/dev/null | grep -q -- '--aws-sigv4' || die "curl is too old for --aws-sigv4 (need >= 7.75.0)"

PUBLIC_BASE="${R2_PUBLIC_BASE:-https://dl.jabali-panel.com}"
PUBLIC_BASE="${PUBLIC_BASE%/}"
DIST_DIR="${DIST_DIR:-dist}"
SHORT_SHA="${SHORT_SHA:-$(git rev-parse --short HEAD)}"
FULL_SHA="${FULL_SHA:-$(git rev-parse HEAD)}"
PUBLISHED_AT="${PUBLISHED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

TARBALL="${DIST_DIR}/jabali-release-${SHORT_SHA}.tar.gz"
CHECKSUM="${TARBALL}.sha256"
[[ -f "$TARBALL" ]]  || die "tarball not found: $TARBALL (run tools/build-release.sh first)"
[[ -f "$CHECKSUM" ]] || die "checksum sidecar not found: $CHECKSUM"

# Verify the sidecar matches the tarball before we publish a pointer to it — a
# corrupt upload that still gets referenced by latest.json would brick installs.
expected="$(awk '{print $1}' "$CHECKSUM")"
actual="$(sha256sum "$TARBALL" | awk '{print $1}')"
[[ -n "$expected" && "$expected" == "$actual" ]] \
  || die "local checksum mismatch (sidecar $expected vs file $actual) — rebuild the release"

ENDPOINT="https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com"

# put_object <local-file> <key> <content-type> <cache-control>
put_object() {
  local file="$1" key="$2" ctype="$3" cache="$4"
  log "PUT ${key} (${ctype})"
  curl -fsS --retry 3 --retry-delay 3 \
    --aws-sigv4 "aws:amz:auto:s3" \
    --user "${R2_ACCESS_KEY_ID}:${R2_SECRET_ACCESS_KEY}" \
    -H "Content-Type: ${ctype}" \
    -H "Cache-Control: ${cache}" \
    -X PUT --data-binary @"$file" \
    "${ENDPOINT}/${R2_BUCKET}/${key}" \
    || die "upload failed: ${key}"
}

IMMUTABLE="public, max-age=31536000, immutable"
POINTER="public, max-age=60, must-revalidate"

put_object "$TARBALL"  "jabali-release-${SHORT_SHA}.tar.gz"        "application/gzip" "$IMMUTABLE"
put_object "$CHECKSUM" "jabali-release-${SHORT_SHA}.tar.gz.sha256" "text/plain"       "$IMMUTABLE"

# The pointer the bootstrap reads FIRST. Written LAST so it never references an
# object that isn't fully uploaded yet.
manifest="$(printf '{\n  "version": "%s",\n  "full_sha": "%s",\n  "tarball": "%s/jabali-release-%s.tar.gz",\n  "sha256": "%s",\n  "published_at": "%s"\n}\n' \
  "$SHORT_SHA" "$FULL_SHA" "$PUBLIC_BASE" "$SHORT_SHA" "$expected" "$PUBLISHED_AT")"
tmp_manifest="$(mktemp)"; trap 'rm -f "$tmp_manifest"' EXIT
printf '%s' "$manifest" > "$tmp_manifest"
put_object "$tmp_manifest" "latest.json" "application/json" "$POINTER"

log "published release ${SHORT_SHA} to ${PUBLIC_BASE}/latest.json"
printf '%s\n' "$manifest"

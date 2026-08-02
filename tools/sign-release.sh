#!/usr/bin/env bash
# Sign a published release with the offline jabali release key (JAB-190).
#
#   tools/sign-release.sh release-1a2b3c4
#
# Run this on the machine that holds the private key — NOT in CI, and not on a
# server. The whole value of the signature is that the key is somewhere the
# release pipeline cannot reach: if CI could sign, a compromised Actions token
# would produce signatures that every box accepts, which is the attack the
# signature exists to stop.
#
# What it does:
#   1. downloads the release's tarball + .sha256
#   2. re-verifies the checksum locally (catches a corrupted download before
#      you sign it — signing a bad artifact is worse than not signing)
#   3. signs the tarball with minisign
#   4. uploads the .minisig back onto the release
#
# Requires: minisign, gh (authenticated), curl.
set -euo pipefail

TAG="${1:-}"
KEY="${JABALI_MINISIGN_KEY:-$HOME/.minisign/jabali-release.key}"
REPO="${JABALI_REPO:-shukiv/jabali-panel}"

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
log() { printf '\033[34m==>\033[0m %s\n' "$*"; }

[[ -n "$TAG" ]] || die "usage: $0 <release-tag>   (e.g. release-1a2b3c4)"
command -v minisign >/dev/null 2>&1 || die "minisign not installed (apt install minisign / brew install minisign)"
command -v gh >/dev/null 2>&1 || die "gh not installed"
[[ -f "$KEY" ]] || die "private key not found at $KEY — set JABALI_MINISIGN_KEY"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Sign every tarball the release carries: the per-commit asset and the
# fixed-name copy the CDN "latest" path serves. `jabali update` fetches one or
# the other depending on how it resolved the release, so signing only one
# leaves half the fleet unable to verify.
mapfile -t assets < <(gh release view "$TAG" --repo "$REPO" --json assets \
  --jq '.assets[].name | select(endswith(".tar.gz"))')
[[ ${#assets[@]} -gt 0 ]] || die "release $TAG has no .tar.gz assets"

for asset in "${assets[@]}"; do
  log "downloading $asset"
  gh release download "$TAG" --repo "$REPO" --pattern "$asset" --dir "$tmp" --clobber
  gh release download "$TAG" --repo "$REPO" --pattern "${asset}.sha256" --dir "$tmp" --clobber \
    || die "$asset has no .sha256 — refusing to sign an artifact the release itself cannot vouch for"

  log "verifying checksum before signing"
  expected="$(awk '{print $1}' "$tmp/${asset}.sha256")"
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
  [[ "$expected" == "$actual" ]] \
    || die "checksum mismatch for $asset (expected $expected, got $actual) — do NOT sign this"

  log "signing $asset"
  # The trusted comment is covered by minisign's second signature, so it cannot
  # be rewritten after the fact; put the identifying facts in it.
  minisign -S -s "$KEY" -m "$tmp/$asset" \
    -t "jabali release $TAG file:$asset sha256:$actual"

  log "uploading ${asset}.minisig"
  gh release upload "$TAG" --repo "$REPO" "$tmp/${asset}.minisig" --clobber
done

log "verifying the uploaded signatures against the public key"
pub="${JABALI_MINISIGN_PUB:-$HOME/.minisign/jabali-release.pub}"
if [[ -f "$pub" ]]; then
  for asset in "${assets[@]}"; do
    minisign -V -p "$pub" -m "$tmp/$asset" >/dev/null \
      || die "self-verification failed for $asset — the release is NOT correctly signed"
  done
  log "all signatures verify"
else
  printf '\033[33mwarning:\033[0m public key not at %s — skipped self-verification\n' "$pub" >&2
fi

log "done. $TAG is signed."

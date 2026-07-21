#!/usr/bin/env bash
# Jabali Panel TUI bootstrap (M353 / GH #353).
#
#   curl -fsSL https://get.jabali-panel.com | sudo bash
#
# Downloads the latest sha256-verified release tarball, extracts the prebuilt
# jabali-installer binary + install.sh, and launches the Bubble Tea installer.
# The installer collects a deploy profile + modules + config, then runs
# install.sh (JABALI_MODULES=…) with a live progress pane.
#
# Release resolution is served from jabali's OWN CDN (Cloudflare R2) via a tiny
# latest.json manifest — NOT the GitHub API. The unauthenticated GitHub API is
# capped at 60 requests/hour/IP, and cloud VMs behind shared-egress IPs hit that
# ceiling constantly ("403 API rate limit exceeded"). The manifest has no such
# limit. GitHub's releases API is kept only as a fallback for the window right
# after a release, before the manifest is published.
#
# Non-interactive parity: pipe with no TTY, pass --unattended, or preset
# JABALI_MODULES and the installer skips the TUI and runs install.sh directly.
# Any args after `bash -s --` are forwarded to the installer (e.g. --dry-run).
#
# Overrides:
#   JABALI_MANIFEST_URL      default https://dl.jabali-panel.com/latest.json
#   JABALI_RELEASE_API_BASE  GitHub fallback repo API (used only if the manifest
#                            is unreachable)
#   JABALI_GITHUB_TOKEN / GITHUB_TOKEN  auth for the GitHub fallback (raises the
#                            60/hr unauthenticated limit to 5000/hr)
set -euo pipefail

MANIFEST_URL="${JABALI_MANIFEST_URL:-https://dl.jabali-panel.com/latest.json}"
API_BASE="${JABALI_RELEASE_API_BASE:-https://api.github.com/repos/shukiv/jabali-panel}"

die() { printf '\033[1;31m[bootstrap] %s\033[0m\n' "$*" >&2; exit 1; }
log() { printf '\033[1;34m[bootstrap]\033[0m %s\n' "$*"; }

[[ $EUID -eq 0 ]] || die "must run as root (curl … | sudo bash)"
for bin in curl tar sha256sum; do
  command -v "$bin" >/dev/null 2>&1 || die "missing required tool: $bin"
done

tmp="$(mktemp -d /tmp/jabali-bootstrap.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT

# Extract one string field from flat JSON with grep/sed — no jq on a fresh box.
json_str() { grep -oE "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | sed -E "s/.*:[[:space:]]*\"([^\"]*)\"/\1/" | head -1; }

tar_url=""      # resolved release tarball URL
sum_url=""      # optional .sha256 sidecar URL (GitHub fallback path)
expected=""     # expected sha256 (from the manifest, or the sidecar)

# ---- primary: jabali CDN manifest (Cloudflare R2, no rate limit) ----------
log "resolving latest release from ${MANIFEST_URL}"
if manifest="$(curl -fsSL --connect-timeout 15 --retry 2 --retry-delay 2 "$MANIFEST_URL" 2>/dev/null)"; then
  tar_url="$(printf '%s' "$manifest" | json_str tarball)"
  expected="$(printf '%s' "$manifest" | json_str sha256)"
  if [[ -z "$tar_url" || -z "$expected" ]]; then
    log "manifest is missing tarball/sha256 — falling back to GitHub releases"
    tar_url="" expected=""
  fi
else
  log "jabali CDN unreachable — falling back to GitHub releases"
fi

# ---- fallback: GitHub releases API (rate-limited; optional token) ----------
# Scan the releases list (newest first) rather than /releases/latest: the very
# newest tag may be published seconds before its build finishes, so it can be
# asset-less. Walk candidates until one has both a tarball and a .sha256 sidecar.
if [[ -z "$tar_url" ]]; then
  auth=()
  tok="${JABALI_GITHUB_TOKEN:-${GITHUB_TOKEN:-}}"
  [[ -n "$tok" ]] && auth=(-H "Authorization: Bearer $tok")
  log "resolving latest release from ${API_BASE}"
  if ! rel="$(curl -fsSL "${auth[@]}" "${API_BASE}/releases?per_page=30" 2>/dev/null)"; then
    code="$(curl -s -o /dev/null -w '%{http_code}' "${auth[@]}" "${API_BASE}/releases?per_page=30" 2>/dev/null || true)"
    if [[ "$code" == "403" ]]; then
      die "GitHub API rate limit hit (60/hr per IP, unauthenticated). Retry within the hour, set JABALI_GITHUB_TOKEN=<token> for 5000/hr, or point JABALI_MANIFEST_URL at the jabali CDN."
    fi
    die "could not reach the release API (HTTP ${code:-?})"
  fi
  tar_urls="$(printf '%s' "$rel" \
    | grep -oE '"browser_download_url": *"[^"]+"' \
    | sed 's/.*"\(https[^"]*\)"/\1/' \
    | grep -E 'jabali-release-[0-9a-f]+\.tar\.gz$' || true)"
  [[ -n "$tar_urls" ]] || die "no published release tarball found (the release build may not have attached assets yet)"
  while IFS= read -r u; do
    [[ -n "$u" ]] || continue
    if curl -fsIL "${u}.sha256" >/dev/null 2>&1; then
      tar_url="$u"
      sum_url="${u}.sha256"
      break
    fi
  done <<< "$tar_urls"
  [[ -n "$tar_url" ]] || die "no release had a matching .tar.gz + .sha256 pair (the release build may be mid-flight — retry shortly)"
fi

# ---- download + verify ----------------------------------------------------
log "downloading $(basename "$tar_url")"
curl -fsSL --connect-timeout 20 --retry 3 --retry-delay 3 "$tar_url" -o "$tmp/release.tar.gz" \
  || die "tarball download failed: $tar_url"
if [[ -z "$expected" && -n "$sum_url" ]]; then
  curl -fsSL "$sum_url" -o "$tmp/release.sha256" || die "checksum download failed: $sum_url"
  expected="$(awk '{print $1}' "$tmp/release.sha256")"
fi
[[ -n "$expected" ]] || die "no expected sha256 for the release (manifest and sidecar both absent)"
actual="$(sha256sum "$tmp/release.tar.gz" | awk '{print $1}')"
[[ "$expected" == "$actual" ]] || die "checksum mismatch (expected $expected, got $actual)"
log "checksum verified"

# Extract only what the bootstrap needs.
tar -C "$tmp" -xzf "$tmp/release.tar.gz" bin/jabali-installer install.sh \
  || die "release tarball is missing bin/jabali-installer or install.sh (rebuild the release)"
chmod +x "$tmp/bin/jabali-installer"

log "launching installer"
exec env JABALI_INSTALL_SH="$tmp/install.sh" "$tmp/bin/jabali-installer" "$@"

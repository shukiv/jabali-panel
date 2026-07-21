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
# Release resolution uses GitHub's releases/latest/download REDIRECT — served by
# github.com, NOT api.github.com — so it does NOT consume the API's 60 req/hr/IP
# unauthenticated limit that made `curl … | bash` fail with "403 API rate limit
# exceeded" on cloud VMs behind shared-egress IPs. No hosting, no token, no cost.
# The GitHub releases API is kept only as a fallback for the brief window right
# after a release, before the fixed-name asset is attached.
#
# Non-interactive parity: pipe with no TTY, pass --unattended, or preset
# JABALI_MODULES and the installer skips the TUI and runs install.sh directly.
# Any args after `bash -s --` are forwarded to the installer (e.g. --dry-run).
#
# Overrides:
#   JABALI_RELEASE_DOWNLOAD_BASE  default
#       https://github.com/shukiv/jabali-panel/releases/latest/download
#   JABALI_RELEASE_API_BASE       GitHub fallback repo API (used only if the
#                                 fixed-name latest asset isn't attached yet)
#   JABALI_GITHUB_TOKEN / GITHUB_TOKEN  auth for the GitHub fallback (raises the
#                                 60/hr unauthenticated limit to 5000/hr)
set -euo pipefail

DL_BASE="${JABALI_RELEASE_DOWNLOAD_BASE:-https://github.com/shukiv/jabali-panel/releases/latest/download}"
API_BASE="${JABALI_RELEASE_API_BASE:-https://api.github.com/repos/shukiv/jabali-panel}"

die() { printf '\033[1;31m[bootstrap] %s\033[0m\n' "$*" >&2; exit 1; }
log() { printf '\033[1;34m[bootstrap]\033[0m %s\n' "$*"; }

[[ $EUID -eq 0 ]] || die "must run as root (curl … | sudo bash)"
for bin in curl tar sha256sum; do
  command -v "$bin" >/dev/null 2>&1 || die "missing required tool: $bin"
done

tmp="$(mktemp -d /tmp/jabali-bootstrap.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT

tar_url=""      # resolved release tarball URL
sum_url=""      # its .sha256 sidecar URL

# ---- primary: releases/latest/download (github.com redirect, no API) -------
# The fixed-name asset "jabali-release.tar.gz" is (re)attached to every release,
# so /releases/latest/download/jabali-release.tar.gz always points at the newest
# published release. -f makes curl fail if the redirect target 404s (the asset
# isn't attached yet), which drops us to the API fallback.
cand="${DL_BASE}/jabali-release.tar.gz"
log "resolving latest release via ${cand}"
if curl -fsIL --connect-timeout 15 "$cand" >/dev/null 2>&1 \
   && curl -fsIL --connect-timeout 15 "${cand}.sha256" >/dev/null 2>&1; then
  tar_url="$cand"
  sum_url="${cand}.sha256"
else
  log "fixed-name latest asset not found — falling back to the GitHub API"
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
      die "GitHub API rate limit hit (60/hr per IP, unauthenticated). Retry within the hour, or set JABALI_GITHUB_TOKEN=<token> for 5000/hr. (The primary releases/latest/download path is not rate-limited — this fallback only runs when the latest asset isn't attached yet.)"
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
curl -fsSL --connect-timeout 20 --retry 3 "$sum_url" -o "$tmp/release.sha256" \
  || die "checksum download failed: $sum_url"
expected="$(awk '{print $1}' "$tmp/release.sha256")"
[[ -n "$expected" ]] || die "empty checksum in $sum_url"
actual="$(sha256sum "$tmp/release.tar.gz" | awk '{print $1}')"
[[ "$expected" == "$actual" ]] || die "checksum mismatch (expected $expected, got $actual)"
log "checksum verified"

# Extract only what the bootstrap needs.
tar -C "$tmp" -xzf "$tmp/release.tar.gz" bin/jabali-installer install.sh \
  || die "release tarball is missing bin/jabali-installer or install.sh (rebuild the release)"
chmod +x "$tmp/bin/jabali-installer"

log "launching installer"
exec env JABALI_INSTALL_SH="$tmp/install.sh" "$tmp/bin/jabali-installer" "$@"

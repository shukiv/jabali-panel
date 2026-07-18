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
# Non-interactive parity: pipe with no TTY, pass --unattended, or preset
# JABALI_MODULES and the installer skips the TUI and runs install.sh directly.
# Any args after `bash -s --` are forwarded to the installer (e.g. --dry-run).
#
# Overrides: JABALI_RELEASE_API_BASE (default GitHub), JABALI_REF (unused here;
# the tarball is always the latest published release).
set -euo pipefail

API_BASE="${JABALI_RELEASE_API_BASE:-https://api.github.com/repos/shukiv/jabali-panel}"

die() { printf '\033[1;31m[bootstrap] %s\033[0m\n' "$*" >&2; exit 1; }
log() { printf '\033[1;34m[bootstrap]\033[0m %s\n' "$*"; }

[[ $EUID -eq 0 ]] || die "must run as root (curl … | sudo bash)"
for bin in curl tar sha256sum; do
  command -v "$bin" >/dev/null 2>&1 || die "missing required tool: $bin"
done

log "resolving latest release with a verified tarball from ${API_BASE}"
# Scan the releases list (newest first) rather than /releases/latest: the very
# newest tag may be published seconds before its build finishes, so it can be
# asset-less. grep/sed only — no jq dependency on a fresh box. The tarball URLs
# are already newest-first; the matching .sha256 sidecar is ALWAYS derived from
# the same tarball URL (same release, same path) so we never pair a tarball with
# a different release's checksum. Walk candidates until one downloads + verifies.
rel="$(curl -fsSL "${API_BASE}/releases?per_page=30")" || die "could not reach the release API"
tar_urls="$(printf '%s' "$rel" \
  | grep -oE '"browser_download_url": *"[^"]+"' \
  | sed 's/.*"\(https[^"]*\)"/\1/' \
  | grep -E 'jabali-release-[0-9a-f]+\.tar\.gz$' || true)"
[[ -n "$tar_urls" ]] || die "no published release tarball found (the release build may not have attached assets yet)"

tmp="$(mktemp -d /tmp/jabali-bootstrap.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT

verified=""
while IFS= read -r tar_url; do
  [[ -n "$tar_url" ]] || continue
  sum_url="${tar_url}.sha256"                       # same release, same path
  log "trying $(basename "$tar_url")"
  curl -fsSL "$sum_url" -o "$tmp/release.sha256" 2>/dev/null || { log "  no checksum sidecar, skipping"; continue; }
  curl -fsSL "$tar_url" -o "$tmp/release.tar.gz"  || { log "  tarball download failed, skipping"; continue; }
  expected="$(awk '{print $1}' "$tmp/release.sha256")"
  actual="$(sha256sum "$tmp/release.tar.gz" | awk '{print $1}')"
  if [[ -n "$expected" && "$expected" == "$actual" ]]; then
    verified=1
    log "checksum verified"
    break
  fi
  log "  checksum mismatch, skipping"
done <<< "$tar_urls"
[[ -n "$verified" ]] || die "no release had a matching .tar.gz + .sha256 pair (the release build may be mid-flight — retry shortly)"

# Extract only what the bootstrap needs.
tar -C "$tmp" -xzf "$tmp/release.tar.gz" bin/jabali-installer install.sh \
  || die "release tarball is missing bin/jabali-installer or install.sh (rebuild the release)"
chmod +x "$tmp/bin/jabali-installer"

log "launching installer"
exec env JABALI_INSTALL_SH="$tmp/install.sh" "$tmp/bin/jabali-installer" "$@"

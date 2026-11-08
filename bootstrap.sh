#!/usr/bin/env bash
# Jabali Panel TUI bootstrap (M353 / GH #353).
#
#   curl -fsSL https://get.jabali-panel.com | sudo bash
#
# Downloads the current stable release's sha256-verified tarball, extracts the
# prebuilt jabali-installer binary + install.sh, and launches the Bubble Tea
# installer.
# The installer collects a deploy profile + modules + config, then runs
# install.sh (JABALI_MODULES=…) with a live progress pane.
#
# Which release: the stable channel's by default, the release built for the
# commit the `stable` tag names, which is what a new server then follows.
# JABALI_RELEASE_CHANNEL=development installs the newest main build instead.
# Neither touches api.github.com, whose 60 req/hr/IP unauthenticated limit made
# `curl … | bash` fail with "403 API rate limit exceeded" on cloud VMs behind
# shared-egress IPs:
#   - stable reads the tag from <repo>.git/info/refs (the ref list `git
#     ls-remote` reads, served by github.com) and fetches release-<short>'s
#     assets by name;
#   - development uses GitHub's releases/latest/download redirect. The
#     releases API is kept only as its fallback for the brief window right
#     after a release, before the fixed-name asset is attached.
#
# Non-interactive parity: pipe with no TTY, pass --unattended, or preset
# JABALI_MODULES and the installer skips the TUI and runs install.sh directly.
# Any args after `bash -s --` are forwarded to the installer (e.g. --dry-run).
#
# Overrides:
#   JABALI_RELEASE_CHANNEL        stable (default) or development
#   JABALI_RELEASE_REPO           default https://github.com/shukiv/jabali-panel
#                                 (the stable channel's tag and assets)
#   JABALI_RELEASE_DOWNLOAD_BASE  default
#       https://github.com/shukiv/jabali-panel/releases/latest/download
#   JABALI_RELEASE_API_BASE       GitHub fallback repo API (development only,
#                                 used only if the fixed-name latest asset
#                                 isn't attached yet)
#   JABALI_GITHUB_TOKEN / GITHUB_TOKEN  auth for the GitHub fallback (raises the
#                                 60/hr unauthenticated limit to 5000/hr)
set -euo pipefail

CHANNEL="${JABALI_RELEASE_CHANNEL:-stable}"
REPO_WEB="${JABALI_RELEASE_REPO:-https://github.com/shukiv/jabali-panel}"
DL_BASE="${JABALI_RELEASE_DOWNLOAD_BASE:-https://github.com/shukiv/jabali-panel/releases/latest/download}"
API_BASE="${JABALI_RELEASE_API_BASE:-https://api.github.com/repos/shukiv/jabali-panel}"

die() { printf '\033[1;31m[bootstrap] %s\033[0m\n' "$*" >&2; exit 1; }
log() { printf '\033[1;34m[bootstrap]\033[0m %s\n' "$*"; }

case "$CHANNEL" in
  stable|development) ;;
  *) die "JABALI_RELEASE_CHANNEL must be stable or development, not '$CHANNEL'" ;;
esac

[[ $EUID -eq 0 ]] || die "must run as root (curl … | sudo bash)"
for bin in curl tar sha256sum; do
  command -v "$bin" >/dev/null 2>&1 || die "missing required tool: $bin"
done

tmp="$(mktemp -d /tmp/jabali-bootstrap.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT

tar_url=""      # resolved release tarball URL
sum_url=""      # its .sha256 sidecar URL
stable_sha=""   # the commit the `stable` tag names (stable channel)

# stable_commit — print the commit the `stable` tag names, from a git ref list
# (the file <repo>.git/info/refs?service=git-upload-pack returns). Each ref is a
# pkt-line, "<4 hex length><40 hex commit> <ref>\n"; an annotated tag is
# followed by its peeled "<ref>^{}" line, which names the commit. Prints
# nothing when there is no `stable` tag.
stable_commit() {
  local c
  c="$(grep -a -m1 -o -E '[0-9a-f]{40} refs/tags/stable\^\{\}$' "$1" | cut -c1-40)" || c=""
  if [[ -z "$c" ]]; then
    c="$(grep -a -m1 -o -E '[0-9a-f]{40} refs/tags/stable$' "$1" | cut -c1-40)" || c=""
  fi
  printf '%s' "$c"
}

# release_for_commit — set tar_url/sum_url to the release built for a commit,
# release-<short sha>. The short length is whatever the release build used (9
# today), so try a few on the small .sha256 sidecar, as `jabali update` does
# (byCommitDownloadURLs in panel-api/cmd/server/update_release_direct.go).
release_for_commit() {
  local n short cand
  for n in 9 8 10 7 11 12; do
    short="${1:0:$n}"
    cand="${REPO_WEB}/releases/download/release-${short}/jabali-release-${short}.tar.gz"
    if curl -fsIL --connect-timeout 15 "${cand}.sha256" >/dev/null 2>&1; then
      tar_url="$cand"
      sum_url="${cand}.sha256"
      return 0
    fi
  done
  return 1
}

# ---- stable: the release built for the commit the `stable` tag names --------
if [[ "$CHANNEL" == "stable" ]]; then
  log "resolving the stable release via ${REPO_WEB}.git/info/refs"
  curl -fsSL --connect-timeout 20 --retry 3 -o "$tmp/refs" \
    "${REPO_WEB}.git/info/refs?service=git-upload-pack" \
    || die "could not read the release tags from ${REPO_WEB}"
  stable_sha="$(stable_commit "$tmp/refs")"
  [[ -n "$stable_sha" ]] \
    || die "no stable release has been published yet (JABALI_RELEASE_CHANNEL=development installs the newest build)"
  release_for_commit "$stable_sha" \
    || die "the stable release (${stable_sha:0:9}) has no published tarball yet — retry shortly"
fi

# ---- development, primary: releases/latest/download (github.com redirect) --
# The fixed-name asset "jabali-release.tar.gz" is (re)attached to every release,
# so /releases/latest/download/jabali-release.tar.gz always points at the newest
# published release. -f makes curl fail if the redirect target 404s (the asset
# isn't attached yet), which drops us to the API fallback.
if [[ "$CHANNEL" == "development" ]]; then
  cand="${DL_BASE}/jabali-release.tar.gz"
  log "resolving latest release via ${cand}"
  if curl -fsIL --connect-timeout 15 "$cand" >/dev/null 2>&1 \
     && curl -fsIL --connect-timeout 15 "${cand}.sha256" >/dev/null 2>&1; then
    tar_url="$cand"
    sum_url="${cand}.sha256"
  else
    log "fixed-name latest asset not found — falling back to the GitHub API"
  fi
fi

# ---- development, fallback: GitHub releases API (rate-limited) -------------
# Scan the releases list (newest first) rather than /releases/latest: the very
# newest tag may be published seconds before its build finishes, so it can be
# asset-less. Walk candidates until one has both a tarball and a .sha256 sidecar.

# release_tarball_urls — read a GitHub releases list (JSON) on stdin and print
# its release tarball URLs, newest release first, skipping pre-releases. A
# hotfix build for the stable channel is a pre-release (release-hotfix.yml): it
# can be newer than the newest main build, but it is not what a fresh install
# gets. The API lists each release's "prerelease" before its assets.
release_tarball_urls() {
  grep -oE '"prerelease": *(true|false)|"browser_download_url": *"[^"]+"' \
    | awk '/^"prerelease"/ { pre = ($0 ~ /true/); next }
           !pre { sub(/^"browser_download_url": *"/, ""); sub(/"$/, ""); print }' \
    | grep -E 'jabali-release-[0-9a-f]+\.tar\.gz$' || true
}

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
  tar_urls="$(printf '%s' "$rel" | release_tarball_urls)"
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
# NOTE (JAB-190 / ADR-0162): this checksum comes from the SAME origin as the
# tarball, so it proves the download was not corrupted — not that the release is
# genuine. Anyone who can publish a release can publish a matching .sha256. A
# fresh `curl | bash` install is trust-on-first-use by nature: verifying a
# signature here would mean first fetching a verifier, which needs trust of its
# own. Updates are different: a panel binary built with the release key
# (releaseSigningPublicKey) makes `jabali update` check the release's minisign
# signature, for the commit it checks out as well as the tarball it installs,
# before running either. Builds without the key accept the checksum alone.

# Extract the whole bin/ — not just the installer. GH #731: the tarball already
# contains compiled jabali-panel / jabali-agent / jabali-ssh-shell /
# jabali-mailhook, and jabali-panel has the built SPA embedded. Handing those to
# install.sh lets it skip `npm ci` + `vite build` + four `go build`s, which is
# what OOM-kills a 2 GB VPS. We used to extract two files and then recompile
# everything we had just downloaded.
tar -C "$tmp" -xzf "$tmp/release.tar.gz" bin install.sh \
  || die "release tarball is missing bin/ or install.sh (rebuild the release)"
[[ -x "$tmp/bin/jabali-installer" ]] || chmod +x "$tmp/bin/jabali-installer" 2>/dev/null || true
[[ -f "$tmp/bin/jabali-installer" ]] \
  || die "release tarball is missing bin/jabali-installer (rebuild the release)"
chmod +x "$tmp/bin"/* 2>/dev/null || true

# MANIFEST carries the release short_sha so the installed binary reports the
# commit it was actually built from. Optional: older tarballs predate it, and
# install.sh falls back to a source build if anything here is missing.
manifest=""
tar -C "$tmp" -xzf "$tmp/release.tar.gz" MANIFEST 2>/dev/null && manifest="$tmp/MANIFEST"

# The stable release is found by its name; its MANIFEST says which commit it
# was built from, and install.sh checks that commit out.
if [[ "$CHANNEL" == "stable" ]]; then
  built_from=""
  [[ -n "$manifest" ]] && built_from="$(awk -F= '/^full_sha=/{print $2; exit}' "$manifest")"
  [[ "$built_from" == "$stable_sha" ]] \
    || die "the release tarball was built from ${built_from:-an unknown commit}, not the stable commit ${stable_sha}"
fi

log "launching installer"
exec env \
  JABALI_INSTALL_SH="$tmp/install.sh" \
  JABALI_PREBUILT_BIN="$tmp/bin" \
  JABALI_RELEASE_MANIFEST="$manifest" \
  JABALI_RELEASE_CHANNEL="$CHANNEL" \
  "$tmp/bin/jabali-installer" "$@"

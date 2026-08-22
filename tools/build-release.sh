#!/usr/bin/env bash
# tools/build-release.sh — produce a self-contained release tarball.
#
# Output:
#   dist/jabali-release-<short_sha>.tar.gz       The bundle
#   dist/jabali-release-<short_sha>.tar.gz.sha256 Checksum (one-liner)
#
# Bundle layout (inside the tarball):
#   bin/jabali-panel        Static linux/amd64 panel-api binary
#   bin/jabali-agent        Static linux/amd64 agent binary
#   bin/jabali-ssh-shell    Static SSH-shell helper
#   MANIFEST                short_sha + full_sha + build_time + go_version
#
# The panel-ui dist tree is embedded into jabali-panel via //go:embed, so
# the binary IS the UI — there is no separate frontend payload.
#
# Why a script and not just CI YAML: same code path runs locally for
# release rehearsal AND in Gitea Actions, so the produced bundle is
# byte-identical regardless of where it was built. Repro a release on
# a workstation by running this script with the same env.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Inputs (CI overrides). short_sha is the human-readable artifact label;
# full_sha is what `jabali version` reports; build_time is RFC3339 UTC.
FULL_SHA="${FULL_SHA:-$(git rev-parse HEAD)}"
SHORT_SHA="${SHORT_SHA:-$(git rev-parse --short HEAD)}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
GO_BIN="${GO_BIN:-go}"
NPM_BIN="${NPM_BIN:-npm}"
DIST_DIR="${DIST_DIR:-${REPO_ROOT}/dist}"

mkdir -p "$DIST_DIR"

echo "==> short_sha=${SHORT_SHA} full_sha=${FULL_SHA} build_time=${BUILD_TIME}"
echo "==> go=$($GO_BIN version)  node=$(node --version)  npm=$($NPM_BIN --version)"

# Stage dir is wiped each run — caller can keep it under DIST_DIR to
# inspect intermediate output.
STAGE="${DIST_DIR}/stage-${SHORT_SHA}"
rm -rf "$STAGE"
mkdir -p "$STAGE/bin"

# 1. Frontend build. //go:embed in panel-ui/embed.go pulls panel-ui/dist
#    into the panel-api binary, so this MUST run before `go build`.
echo "==> npm ci"
(cd panel-ui && $NPM_BIN ci --no-audit --no-fund)

echo "==> vite build (cap heap to 75% of total RAM, swap-friendly)"
TOTAL_MB="$(awk '/^MemTotal:/ {printf "%d", $2/1024}' /proc/meminfo 2>/dev/null || echo 4096)"
HEAP_MB=$(( TOTAL_MB * 75 / 100 ))
(cd panel-ui && NODE_OPTIONS="--max-old-space-size=${HEAP_MB}" $NPM_BIN run build)

# 2. Go builds. ldflags match install.sh's build_backend so api.Version
#    + api.Commit + api.BuildTime survive both code paths. panel-agent
#    only carries main.version today; the agent doesn't expose
#    build-info over JMAP/HTTP yet.
LDFLAGS_API="-s -w"
LDFLAGS_API+=" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.Version=${SHORT_SHA}"
LDFLAGS_API+=" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.Commit=${FULL_SHA}"
LDFLAGS_API+=" -X git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api.BuildTime=${BUILD_TIME}"
LDFLAGS_AGENT="-s -w -X main.version=${SHORT_SHA}"

echo "==> go build jabali-panel"
$GO_BIN build -trimpath -ldflags "$LDFLAGS_API" \
  -o "$STAGE/bin/jabali-panel" ./panel-api/cmd/server

echo "==> go build jabali-agent"
$GO_BIN build -trimpath -ldflags "$LDFLAGS_AGENT" \
  -o "$STAGE/bin/jabali-agent" ./panel-agent/cmd/jabali-agent

echo "==> go build jabali-ssh-shell"
$GO_BIN build -trimpath -ldflags "-s -w" \
  -o "$STAGE/bin/jabali-ssh-shell" ./panel-agent/cmd/jabali-ssh-shell

echo "==> go build jabali-mailhook"
$GO_BIN build -trimpath -ldflags "$LDFLAGS_AGENT" \
  -o "$STAGE/bin/jabali-mailhook" ./panel-agent/cmd/jabali-mailhook

echo "==> go build jabali-sendmail"
$GO_BIN build -trimpath -ldflags "-s -w" \
  -o "$STAGE/bin/jabali-sendmail" ./panel-agent/cmd/jabali-sendmail

echo "==> go build jabali-webdav"
$GO_BIN build -trimpath -ldflags "-s -w" \
  -o "$STAGE/bin/jabali-webdav" ./panel-agent/cmd/jabali-webdav

echo "==> go build jabali-installer"
$GO_BIN build -trimpath -ldflags "-s -w" \
  -o "$STAGE/bin/jabali-installer" ./installer/cmd/jabali-installer

chmod 0755 "$STAGE"/bin/*

# 2b. Bundle the jabali-cache WordPress plugin (GH #406). The agent installs
#     it FROM /usr/local/share/jabali/wp-plugins; ship it in the release so the
#     tarball-update path matches install.sh's source-build bundle.
if [[ -d "${REPO_ROOT}/wp-plugins/jabali-cache" ]]; then
  # GH #627: fail the build if the plugin version / wp.org metadata drifts.
  bash "${REPO_ROOT}/wp-plugins/jabali-cache/bin/check-version.sh" "${REPO_ROOT}/wp-plugins/jabali-cache"
  mkdir -p "$STAGE/wp-plugins"
  # cp, not rsync — the host CI runner has no rsync (build-release.sh used none
  # before #406). The plugin tree carries no nested .git, so a plain copy is fine.
  cp -a "${REPO_ROOT}/wp-plugins/jabali-cache" "$STAGE/wp-plugins/"
  echo "==> staged wp-plugins/jabali-cache"
fi

# 2c. Bundle the docker-app catalog (M48). Same gap as wp-plugins: the panel-api
#     reads it from /usr/local/share/jabali/docker-apps at startup, but only
#     install.sh's build_backend synced it — so a tarball update left the
#     marketplace catalog stale. Ship it in the release.
if [[ -d "${REPO_ROOT}/install/docker-apps" ]]; then
  cp -a "${REPO_ROOT}/install/docker-apps" "$STAGE/docker-apps"
  echo "==> staged docker-apps catalog"
fi

# 2d. Bundle install.sh so the TUI bootstrap (curl|bash) can extract + run it
#     from the ONE sha-verified release tarball (installer binary lives in bin/).
cp -a "${REPO_ROOT}/install.sh" "$STAGE/install.sh"
echo "==> staged install.sh"

# 3. MANIFEST: machine-readable, single line per key. update.go parses
#    this to print "Updating to release <short_sha> (built <build_time>)".
cat > "$STAGE/MANIFEST" <<MANIFEST
short_sha=${SHORT_SHA}
full_sha=${FULL_SHA}
build_time=${BUILD_TIME}
go_version=$($GO_BIN version | awk '{print $3}')
node_version=$(node --version)
release_format=v1
MANIFEST

# 4. Tar + sha256.
TAR_NAME="jabali-release-${SHORT_SHA}.tar.gz"
echo "==> packing $TAR_NAME"
tar -C "$STAGE" -czf "$DIST_DIR/$TAR_NAME" bin MANIFEST install.sh $([[ -d "$STAGE/wp-plugins" ]] && echo wp-plugins) $([[ -d "$STAGE/docker-apps" ]] && echo docker-apps)

(cd "$DIST_DIR" && sha256sum "$TAR_NAME" > "${TAR_NAME}.sha256")
SIZE_MB=$(du -m "$DIST_DIR/$TAR_NAME" | awk '{print $1}')
echo "==> done: $DIST_DIR/$TAR_NAME (${SIZE_MB}M)"
echo "    $(cat "$DIST_DIR/${TAR_NAME}.sha256")"

# Clean stage by default — keep with KEEP_STAGE=1 for inspection.
if [[ -z "${KEEP_STAGE:-}" ]]; then
  rm -rf "$STAGE"
fi

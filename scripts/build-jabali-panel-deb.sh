#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(cat "$ROOT_DIR/VERSION")"
if [[ "$VERSION" == VERSION=* ]]; then
  VERSION="${VERSION#VERSION=}"
fi

if [ ! -d "$ROOT_DIR/vendor" ]; then
  echo "vendor directory missing. Run composer install before building the package." >&2
  exit 1
fi

if [ ! -d "$ROOT_DIR/public/build" ]; then
  echo "public/build missing. Run npm install && npm run build before building the package." >&2
  exit 1
fi

BUILD_DIR="$(mktemp -d)"
PKG_DIR="$BUILD_DIR/jabali-panel"
APP_DIR="$PKG_DIR/var/www/jabali"

mkdir -p "$PKG_DIR/DEBIAN" "$APP_DIR"

cp -a "$ROOT_DIR/packaging/jabali-panel/DEBIAN/." "$PKG_DIR/DEBIAN/"

sed -i "s/^Version:.*/Version: ${VERSION}/" "$PKG_DIR/DEBIAN/control"

tar --exclude='.git' \
    --exclude='.env' \
    --exclude='.phpunit.result.cache' \
    --exclude='.git-credentials' \
    --exclude='.claude' \
    --exclude='node_modules' \
    --exclude='storage' \
    --exclude='database/database.sqlite' \
    --exclude='packaging' \
    --exclude='scripts' \
    --exclude='tests' \
    -C "$ROOT_DIR" -cf - . | tar -C "$APP_DIR" -xf -

if [ -d "$ROOT_DIR/packaging/jabali-panel/etc" ]; then
  cp -a "$ROOT_DIR/packaging/jabali-panel/etc" "$PKG_DIR/"
fi

chmod 0755 "$PKG_DIR/DEBIAN/postinst" "$PKG_DIR/DEBIAN/prerm" "$PKG_DIR/DEBIAN/postrm"

OUTPUT="$ROOT_DIR/jabali-panel_${VERSION}_all.deb"
dpkg-deb --build "$PKG_DIR" "$OUTPUT"

echo "Built: $OUTPUT"

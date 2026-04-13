#!/bin/bash
# Build .deb package for jabali-security
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
VERSION=$(python3 -c "import re; print(re.search(r'version = \"(.+?)\"', open('$PROJECT_DIR/pyproject.toml').read()).group(1))")

PACKAGE="jabali-security"
INSTALL_DIR="/usr/local/jabali-security"
BUILD_DIR="$PROJECT_DIR/build/deb"

echo "Building $PACKAGE $VERSION..."

# Clean
rm -rf "$BUILD_DIR"

# Create directory structure
mkdir -p "$BUILD_DIR/DEBIAN"
mkdir -p "$BUILD_DIR$INSTALL_DIR"/{daemon,lib,api,rules,etc,bin}

# Copy application files
cp -r "$PROJECT_DIR/daemon/"*.py "$BUILD_DIR$INSTALL_DIR/daemon/"
cp -r "$PROJECT_DIR/lib/" "$BUILD_DIR$INSTALL_DIR/lib/"
cp -r "$PROJECT_DIR/api/"*.py "$BUILD_DIR$INSTALL_DIR/api/"
cp -r "$PROJECT_DIR/rules/"*.yar "$BUILD_DIR$INSTALL_DIR/rules/"
cp "$PROJECT_DIR/etc/jabali-security.conf.example" "$BUILD_DIR$INSTALL_DIR/etc/"
cp "$PROJECT_DIR/etc/jabali-security.service" "$BUILD_DIR$INSTALL_DIR/etc/"
cp "$PROJECT_DIR/bin/jabali-security" "$BUILD_DIR$INSTALL_DIR/bin/"
chmod +x "$BUILD_DIR$INSTALL_DIR/bin/jabali-security"

# Create __init__.py files
touch "$BUILD_DIR$INSTALL_DIR/daemon/__init__.py"
touch "$BUILD_DIR$INSTALL_DIR/api/__init__.py"

# Create symlink for CLI
mkdir -p "$BUILD_DIR/usr/local/bin"
ln -sf "$INSTALL_DIR/bin/jabali-security" "$BUILD_DIR/usr/local/bin/jabali-security"

# Process debian files
sed "s/{{VERSION}}/$VERSION/" "$PROJECT_DIR/debian/control" > "$BUILD_DIR/DEBIAN/control"
cp "$PROJECT_DIR/debian/postinst" "$BUILD_DIR/DEBIAN/postinst"
cp "$PROJECT_DIR/debian/prerm" "$BUILD_DIR/DEBIAN/prerm"
cp "$PROJECT_DIR/debian/postrm" "$BUILD_DIR/DEBIAN/postrm"
chmod 755 "$BUILD_DIR/DEBIAN/postinst" "$BUILD_DIR/DEBIAN/prerm" "$BUILD_DIR/DEBIAN/postrm"

# Build the package
mkdir -p "$PROJECT_DIR/dist"
dpkg-deb --build "$BUILD_DIR" "$PROJECT_DIR/dist/${PACKAGE}_${VERSION}_all.deb"

echo "Built: dist/${PACKAGE}_${VERSION}_all.deb"

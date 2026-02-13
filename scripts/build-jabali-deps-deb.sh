#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VERSION="${1:-}"
if [[ -z "$VERSION" && -f "${ROOT_DIR}/VERSION" ]]; then
    VERSION="$(sed -n 's/^VERSION=//p' "${ROOT_DIR}/VERSION")"
fi
VERSION="${VERSION:-0.9-rc36}"

CONTROL_DIR="${ROOT_DIR}/packaging/jabali-deps/DEBIAN"
OUT_DIR="${ROOT_DIR}/packaging/dist"

mkdir -p "${CONTROL_DIR}" "${OUT_DIR}"

sed -i "s/^Version:.*/Version: ${VERSION}/" "${CONTROL_DIR}/control"

PACKAGE_NAME="jabali-deps_${VERSION}_all.deb"

dpkg-deb --build "${ROOT_DIR}/packaging/jabali-deps" "${OUT_DIR}/${PACKAGE_NAME}"

echo "Built: ${OUT_DIR}/${PACKAGE_NAME}"

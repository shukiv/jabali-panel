#!/usr/bin/env bash
set -euo pipefail
# One-line installer for jabali-backup
# Usage: curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-backup/main/get.sh | sudo bash

REPO="https://github.com/shukiv/jabali-backup.git"
DEST="/opt/jabali-backup"

if [[ $EUID -ne 0 ]]; then
    echo "[x] Run as root: curl -fsSL ... | sudo bash" >&2
    exit 1
fi

# Install git if missing
if ! command -v git &>/dev/null; then
    apt-get update -qq && apt-get install -y -qq git
fi

if [[ -d "$DEST/.git" ]]; then
    echo "[+] Updating existing installation..."
    git -C "$DEST" pull --ff-only
    exec "$DEST/install.sh" update
else
    echo "[+] Cloning jabali-backup..."
    git clone "$REPO" "$DEST"
    exec "$DEST/install.sh"
fi

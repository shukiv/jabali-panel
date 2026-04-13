"""Machine identity — single source of truth for the {user}-php naming convention."""

from __future__ import annotations

from pathlib import Path

from jabali_isolator import config

SUFFIX = "-php"


def machine_name(user: str) -> str:
    return f"{user}{SUFFIX}"


def service_name(user: str) -> str:
    return f"systemd-nspawn@{machine_name(user)}.service"


def rootfs_dir(user: str) -> Path:
    return Path(config.MACHINES_DIR) / machine_name(user)


def nspawn_path(user: str) -> Path:
    return Path(config.NSPAWN_DIR) / f"{machine_name(user)}.nspawn"


def dropin_dir(user: str) -> Path:
    return Path(config.SERVICE_DROPIN_BASE) / f"{service_name(user)}.d"

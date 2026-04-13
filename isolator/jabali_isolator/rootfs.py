"""Minimal rootfs builder for nspawn containers.

Creates the bare directory skeleton that systemd-nspawn needs.  Actual binaries
come via read-only bind mounts from the host (/usr, /lib, /bin, etc.), so the
rootfs only contains /etc files and empty mount-point directories.
"""

from __future__ import annotations

import grp
import logging
import os
import pwd
import shutil
from pathlib import Path

from jabali_isolator.machine import rootfs_dir as _machine_dir

logger = logging.getLogger(__name__)


def _lookup_user(user: str) -> pwd.struct_passwd:
    """Look up user in host /etc/passwd.  Raises KeyError if not found."""
    return pwd.getpwnam(user)


def _lookup_group(gid: int) -> grp.struct_group:
    """Look up group by GID."""
    return grp.getgrgid(gid)


def _write_minimal_passwd(root: Path, pw: pwd.struct_passwd) -> None:
    """Write /etc/passwd with root + the target user only."""
    etc = root / "etc"
    etc.mkdir(parents=True, exist_ok=True)

    lines = [
        "root:x:0:0:root:/root:/usr/sbin/nologin",
        f"{pw.pw_name}:x:{pw.pw_uid}:{pw.pw_gid}:{pw.pw_gecos}:{pw.pw_dir}:/usr/sbin/nologin",
    ]
    passwd_file = etc / "passwd"
    passwd_file.write_text("\n".join(lines) + "\n")
    os.chmod(passwd_file, 0o644)


def _write_minimal_group(root: Path, pw: pwd.struct_passwd) -> None:
    """Write /etc/group with root, www-data, and the target user's primary group."""
    etc = root / "etc"
    etc.mkdir(parents=True, exist_ok=True)

    try:
        gr = _lookup_group(pw.pw_gid)
        group_name = gr.gr_name
    except KeyError:
        group_name = pw.pw_name

    lines = [
        "root:x:0:",
        "www-data:x:33:",
    ]
    # Avoid duplicate if user's primary group is www-data
    if pw.pw_gid != 33:
        lines.append(f"{group_name}:x:{pw.pw_gid}:")

    group_file = etc / "group"
    group_file.write_text("\n".join(lines) + "\n")
    os.chmod(group_file, 0o644)


def _copy_resolv_conf(root: Path) -> None:
    """Copy host resolv.conf for DNS resolution inside the container."""
    host_resolv = Path("/etc/resolv.conf")
    dest = root / "etc" / "resolv.conf"
    dest.parent.mkdir(parents=True, exist_ok=True)

    if host_resolv.is_symlink():
        # Resolve symlink (common on systems using systemd-resolved)
        resolved = host_resolv.resolve()
        if resolved.is_file():
            shutil.copy2(str(resolved), str(dest))
        else:
            logger.warning("Host /etc/resolv.conf is a broken symlink, using fallback DNS resolvers")
            dest.write_text("nameserver 1.1.1.1\nnameserver 8.8.8.8\n")
    elif host_resolv.is_file():
        shutil.copy2(str(host_resolv), str(dest))
    else:
        logger.warning("Host /etc/resolv.conf not found, using fallback DNS resolvers (1.1.1.1, 8.8.8.8)")
        dest.write_text("nameserver 1.1.1.1\nnameserver 8.8.8.8\n")


def _copy_os_release(root: Path) -> None:
    """Copy host /etc/os-release so tools like VS Code detect Linux correctly."""
    host_os_release = Path("/etc/os-release")
    dest = root / "etc" / "os-release"
    dest.parent.mkdir(parents=True, exist_ok=True)

    if host_os_release.is_symlink():
        resolved = host_os_release.resolve()
        if resolved.is_file():
            shutil.copy2(str(resolved), str(dest))
            return
    elif host_os_release.is_file():
        shutil.copy2(str(host_os_release), str(dest))
        return

    dest.write_text('ID=linux\nNAME="Linux"\n')


def _create_directories(root: Path, user: str) -> None:
    """Create empty directories that serve as mount points or private dirs."""
    dirs = [
        root / "tmp",
        root / "home" / user,
        root / "usr",
        root / "lib",
        root / "lib64",
        root / "bin",
        root / "sbin",
        root / "etc" / "php",
        root / "etc" / "ssl" / "certs",
        root / "var" / "log",
        root / "var" / "run",
        root / "dev",
    ]
    for d in dirs:
        d.mkdir(parents=True, exist_ok=True)

    # /tmp needs sticky bit
    os.chmod(root / "tmp", 0o1777)  # noqa: S103


def create_rootfs(user: str) -> Path:
    """Build a minimal rootfs for the given user.

    Returns the rootfs path.  Raises KeyError if user does not exist on the host.
    """
    pw = _lookup_user(user)
    root = _machine_dir(user)

    if root.exists():
        logger.info("Rootfs already exists at %s — rebuilding /etc", root)
    else:
        root.mkdir(parents=True)
        logger.info("Created rootfs at %s", root)

    _write_minimal_passwd(root, pw)
    _write_minimal_group(root, pw)
    _copy_resolv_conf(root)
    _copy_os_release(root)
    _create_directories(root, user)

    return root


def destroy_rootfs(user: str) -> bool:
    """Remove the rootfs for a user.  Returns True if removed, False if it didn't exist."""
    from jabali_isolator import config

    root = _machine_dir(user)
    if not root.exists():
        return False

    # Verify resolved path stays under MACHINES_DIR to prevent symlink attacks
    resolved = root.resolve()
    expected_parent = Path(config.MACHINES_DIR).resolve()
    if not resolved.is_relative_to(expected_parent):
        raise RuntimeError(f"Rootfs path escapes MACHINES_DIR: {root} -> {resolved}")

    shutil.rmtree(root)
    logger.info("Removed rootfs at %s", root)
    return True


def rootfs_exists(user: str) -> bool:
    return _machine_dir(user).is_dir()

"""CMS-specific malware cleanup."""
from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import os
from pathlib import Path
from urllib.request import Request, urlopen

from lib.cleanup.injection_patterns import InjectionCleaner
from lib.cleanup.models import CleanupResult

logger = logging.getLogger(__name__)


class CMSCleaner:
    """Detect CMS type and perform CMS-aware cleanup."""

    def __init__(self, use_checksums: bool = True) -> None:
        self._use_checksums = use_checksums
        self._injection_cleaner = InjectionCleaner()

    @staticmethod
    def detect_cms(path: str) -> str | None:
        """Detect CMS from directory structure. Returns 'wordpress', 'joomla', or None."""
        p = Path(path)
        # Walk up to find CMS root
        check_dir = p if p.is_dir() else p.parent
        for _ in range(5):  # Max 5 levels up
            if (check_dir / "wp-config.php").exists():
                return "wordpress"
            if (check_dir / "configuration.php").exists() and (check_dir / "administrator").is_dir():
                return "joomla"
            if check_dir == check_dir.parent:
                break
            check_dir = check_dir.parent
        return None

    @staticmethod
    def find_cms_root(path: str) -> Path | None:
        """Find the CMS installation root directory."""
        p = Path(path)
        check_dir = p if p.is_dir() else p.parent
        for _ in range(5):
            if (check_dir / "wp-config.php").exists():
                return check_dir
            if (check_dir / "configuration.php").exists():
                return check_dir
            if check_dir == check_dir.parent:
                break
            check_dir = check_dir.parent
        return None

    async def clean_file(self, path: str) -> CleanupResult:
        """Clean a single file using CMS-aware or generic injection removal."""
        from lib.tenant import resolve_user

        p = Path(path)
        if not p.is_file():
            return CleanupResult(path=path, strategy="error", success=False, error="File not found")

        if p.is_symlink():
            return CleanupResult(path=path, strategy="error", success=False, error="Symlinks not allowed")

        username = resolve_user(path)

        # Detect CMS
        cms = self.detect_cms(path)

        # Read content via O_NOFOLLOW to prevent TOCTOU symlink races
        try:
            fd = os.open(str(p), os.O_RDONLY | os.O_NOFOLLOW)
            try:
                content = os.read(fd, 50_000_000)  # 50MB max
            finally:
                os.close(fd)
        except OSError as exc:
            return CleanupResult(path=path, strategy="error", success=False, error=str(exc), username=username)

        # Try CMS-specific cleanup first
        if cms == "wordpress" and self._use_checksums:
            result = await self._clean_wordpress_file(p, content, username)
            if result:
                return result

        # Fall back to generic injection removal
        return await self._clean_generic(p, content, username)

    async def _clean_wordpress_file(self, p: Path, content: bytes, username: str | None) -> CleanupResult | None:
        """Clean a WordPress file using core checksums if available."""
        cms_root = self.find_cms_root(str(p))
        if not cms_root:
            return None

        # Check if this is a core WordPress file
        rel_path = None
        try:
            rel_path = str(p.relative_to(cms_root))
        except ValueError:
            return None

        # Get WordPress version
        wp_version = self._get_wp_version(cms_root)
        if not wp_version:
            return None

        # Fetch checksums from WordPress API
        checksums = await asyncio.to_thread(self._fetch_wp_checksums, wp_version)
        if not checksums:
            return None

        expected_hash = checksums.get(rel_path)
        if not expected_hash:
            return None  # Not a core file — use generic cleanup

        actual_hash = hashlib.md5(content).hexdigest()  # noqa: S324
        if actual_hash == expected_hash:
            return None  # File is clean

        # Core file is modified — restore from checksums won't work,
        # but we know it's been tampered. Use injection removal.
        logger.info("WordPress core file modified: %s (expected %s, got %s)", rel_path, expected_hash, actual_hash)
        return await self._clean_generic(p, content, username, strategy="cms_wordpress")

    @staticmethod
    def _get_wp_version(cms_root: Path) -> str | None:
        """Extract WordPress version from wp-includes/version.php."""
        version_file = cms_root / "wp-includes" / "version.php"
        if not version_file.is_file():
            return None
        try:
            import re
            content = version_file.read_text(encoding="utf-8", errors="replace")
            m = re.search(r"\$wp_version\s*=\s*['\"]([^'\"]+)['\"]", content)
            return m.group(1) if m else None
        except OSError:
            return None

    @staticmethod
    def _fetch_wp_checksums(version: str) -> dict[str, str] | None:
        """Fetch WordPress core file checksums from api.wordpress.org."""
        url = "https://api.wordpress.org/core/checksums/1.0/?version=%s&locale=en_US" % version
        try:
            req = Request(url, headers={"User-Agent": "jabali-security"})  # noqa: S310
            with urlopen(req, timeout=15) as resp:  # noqa: S310
                data = json.loads(resp.read().decode())
            if data.get("checksums"):
                return data["checksums"]
        except (OSError, json.JSONDecodeError, KeyError):
            logger.debug("Failed to fetch WordPress checksums for v%s", version)
        return None

    async def _clean_generic(
        self, p: Path, content: bytes, username: str | None, strategy: str = "injection_removal"
    ) -> CleanupResult:
        """Generic injection removal using pattern matching."""
        detections = self._injection_cleaner.detect(content)
        if not detections:
            return CleanupResult(
                path=str(p), strategy=strategy, success=False,
                error="No known injection patterns found", username=username,
            )

        # Create backup before cleaning (O_CREAT|O_EXCL prevents symlink attack)
        backup_path = str(p) + ".jabali-backup"
        try:
            bfd = os.open(backup_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
            try:
                os.write(bfd, content)
            finally:
                os.close(bfd)
        except OSError:
            backup_path = ""

        # Clean
        cleaned, changes = self._injection_cleaner.clean(content)

        if cleaned == content:
            return CleanupResult(
                path=str(p), strategy=strategy, success=False,
                error="Cleaning produced no changes", username=username,
                backup_path=backup_path,
            )

        # Write cleaned content
        try:
            await asyncio.to_thread(p.write_bytes, cleaned)
        except OSError as exc:
            return CleanupResult(
                path=str(p), strategy=strategy, success=False,
                error="Failed to write cleaned file: %s" % exc, username=username,
                backup_path=backup_path,
            )

        logger.info("Cleaned %s: %d changes", p, len(changes))
        return CleanupResult(
            path=str(p), strategy=strategy, success=True,
            backup_path=backup_path, changes_made=changes, username=username,
        )

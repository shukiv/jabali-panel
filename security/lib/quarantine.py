"""Quarantine manager — isolate malicious files safely."""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import os
import shutil
from datetime import datetime, timezone
from pathlib import Path

from lib.models import Incident, QuarantineRecord

logger = logging.getLogger(__name__)


class QuarantineManager:
    # Restore allow-list. Only files under these prefixes may be restored
    # to their original location — blocks an attacker from using the
    # quarantine-restore RPC to drop files into /etc, /usr, or /root.
    # Tests override this via the `restore_allowlist` constructor kwarg
    # because pytest's tmp_path fixture gives paths under /tmp/pytest-*,
    # which is not (and should not be) allowed in production.
    DEFAULT_RESTORE_ALLOWLIST: tuple[str, ...] = ("/home/", "/var/www/")

    def __init__(
        self,
        base_dir: str,
        restore_allowlist: tuple[str, ...] | None = None,
    ) -> None:
        self._base = Path(base_dir)
        self._restore_allowlist = restore_allowlist or self.DEFAULT_RESTORE_ALLOWLIST

    async def quarantine_file(self, path: str, incident: Incident) -> QuarantineRecord | None:
        """Move a file to quarantine. Returns record or None on failure."""
        src = Path(path)
        if not src.is_file():
            logger.warning("Cannot quarantine: file not found: %s", path)
            return None

        # Reject symlinks to prevent quarantining system files
        if src.is_symlink():
            logger.warning("Cannot quarantine symlink: %s", path)
            return None

        # Build quarantine path: base/{user}/{YYYYMMDD}/{filename}
        user = incident.username or "_system"
        date_dir = datetime.now(timezone.utc).strftime("%Y%m%d")
        dest_dir = self._base / user / date_dir
        await asyncio.to_thread(dest_dir.mkdir, parents=True, exist_ok=True)

        # Handle name collisions by appending incident ID
        dest_name = src.name
        dest = dest_dir / dest_name
        if dest.exists():
            dest = dest_dir / ("%s_%s" % (incident.id[:8], dest_name))

        try:
            # Read content via O_NOFOLLOW to prevent symlink races,
            # then write to dest and unlink src (avoids TOCTOU in shutil.move)
            fd = os.open(str(src), os.O_RDONLY | os.O_NOFOLLOW)
            try:
                content = await asyncio.to_thread(os.read, fd, 50_000_000)  # 50MB max
            finally:
                os.close(fd)
            file_hash = hashlib.sha256(content).hexdigest()

            # Write content to quarantine destination (atomic: no symlink race)
            await asyncio.to_thread(dest.write_bytes, content)
            # Remove original
            await asyncio.to_thread(src.unlink)

            # Remove all permissions (make inaccessible)
            await asyncio.to_thread(os.chmod, str(dest), 0o000)

            record = QuarantineRecord(
                original_path=str(src),
                quarantine_path=str(dest),
                username=incident.username,
                reason=incident.summary,
                incident_id=incident.id,
                sha256=file_hash,
            )

            # Write metadata sidecar
            meta_path = Path(str(dest) + ".meta.json")
            await asyncio.to_thread(
                meta_path.write_text,
                record.model_dump_json(indent=2),
                encoding="utf-8",
            )
            await asyncio.to_thread(os.chmod, str(meta_path), 0o600)

            logger.info("Quarantined: %s -> %s (sha256=%s)", path, dest, file_hash[:16])
            return record

        except (PermissionError, OSError):
            logger.exception("Failed to quarantine: %s", path)
            return None

    async def restore_file(self, record: QuarantineRecord) -> bool:
        """Restore a quarantined file to its original location."""
        src = Path(record.quarantine_path)
        dest = Path(record.original_path)

        # Validate destination is under allowed directories (prevent restoring to system paths)
        resolved_dest = str(dest.resolve())
        if not any(resolved_dest.startswith(p) for p in self._restore_allowlist):
            logger.warning("Cannot restore to unsafe path: %s", resolved_dest)
            return False

        if not src.exists():
            logger.warning("Quarantine file missing: %s", src)
            return False

        if dest.exists():
            logger.warning("Cannot restore: original path already exists: %s", dest)
            return False

        try:
            # Restore permissions before moving (need read access)
            await asyncio.to_thread(os.chmod, str(src), 0o644)
            await asyncio.to_thread(dest.parent.mkdir, parents=True, exist_ok=True)
            await asyncio.to_thread(shutil.move, str(src), str(dest))

            # Remove metadata sidecar
            meta = Path(str(src) + ".meta.json")
            if meta.exists():
                await asyncio.to_thread(meta.unlink)

            logger.info("Restored: %s -> %s", src, dest)
            return True
        except (PermissionError, OSError):
            logger.exception("Failed to restore: %s -> %s", src, dest)
            return False

    async def delete_quarantined(self, record: QuarantineRecord) -> bool:
        """Permanently delete a quarantined file."""
        src = Path(record.quarantine_path)
        try:
            if src.exists():
                await asyncio.to_thread(os.chmod, str(src), 0o600)  # need write to delete
                await asyncio.to_thread(src.unlink)
            meta = Path(str(src) + ".meta.json")
            if meta.exists():
                await asyncio.to_thread(meta.unlink)
            logger.info("Deleted quarantined file: %s", src)
            return True
        except OSError:
            logger.exception("Failed to delete quarantined: %s", src)
            return False

    async def count(self) -> int:
        """Count total quarantined files (not metadata)."""
        if not self._base.is_dir():
            return 0

        def _count_files() -> int:
            total = 0
            for _root, _dirs, files in os.walk(str(self._base)):
                for f in files:
                    if not f.endswith(".meta.json"):
                        total += 1
            return total

        return await asyncio.to_thread(_count_files)

    async def list_records(self, username: str | None = None) -> list[QuarantineRecord]:
        """List quarantine records from metadata sidecar files."""
        records: list[QuarantineRecord] = []
        if not self._base.is_dir():
            return records

        def _read_records() -> list[QuarantineRecord]:
            result: list[QuarantineRecord] = []
            for root, _dirs, files in os.walk(str(self._base)):
                for f in files:
                    if not f.endswith(".meta.json"):
                        continue
                    meta_path = Path(root) / f
                    try:
                        data = json.loads(meta_path.read_text(encoding="utf-8"))
                        record = QuarantineRecord.model_validate(data)
                        if username and record.username != username:
                            continue
                        result.append(record)
                    except (json.JSONDecodeError, OSError, ValueError):
                        continue
            return result

        records = await asyncio.to_thread(_read_records)
        return sorted(records, key=lambda r: r.timestamp, reverse=True)

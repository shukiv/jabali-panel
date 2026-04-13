"""OWASP CRS updater -- download latest Core Rule Set from GitHub."""
from __future__ import annotations

import asyncio
import json
import logging
import shutil
import tempfile
from pathlib import Path
from urllib.request import Request, urlopen

logger = logging.getLogger(__name__)

_CRS_RELEASES_URL = "https://api.github.com/repos/coreruleset/coreruleset/releases/latest"


class CRSUpdater:
    """Download and install OWASP Core Rule Set updates."""

    def __init__(self, rules_dir: str) -> None:
        self._rules_dir = Path(rules_dir)

    async def update(self) -> dict:
        """Check for and install CRS update. Returns status dict."""
        return await asyncio.to_thread(self._update_sync)

    def _update_sync(self) -> dict:
        """Synchronous CRS update -- runs in executor."""
        try:
            # Get latest release info
            req = Request(_CRS_RELEASES_URL, headers={"User-Agent": "jabali-security"})  # noqa: S310
            with urlopen(req, timeout=30) as resp:  # noqa: S310
                release = json.loads(resp.read().decode())

            tag = release.get("tag_name", "")
            logger.info("Latest CRS release: %s", tag)

            # Find tarball asset
            tarball_url = release.get("tarball_url")
            if not tarball_url:
                return {"success": False, "error": "No tarball URL in release"}

            # Download to temp
            tmp_dir = tempfile.mkdtemp(prefix="jabali-crs-")
            try:
                tarball_path = Path(tmp_dir) / "crs.tar.gz"
                req = Request(tarball_url, headers={"User-Agent": "jabali-security"})  # noqa: S310
                with urlopen(req, timeout=120) as resp:  # noqa: S310
                    tarball_path.write_bytes(resp.read())

                # Extract
                import tarfile
                with tarfile.open(tarball_path) as tf:
                    tf.extractall(tmp_dir, filter="data")  # noqa: S202

                # Find the extracted rules directory
                extracted = [d for d in Path(tmp_dir).iterdir() if d.is_dir() and d.name != "__MACOSX"]
                if not extracted:
                    return {"success": False, "error": "No directory found in tarball"}

                rules_src = extracted[0] / "rules"
                if not rules_src.is_dir():
                    return {"success": False, "error": "No rules/ directory in CRS"}

                # Install rules
                self._rules_dir.mkdir(parents=True, exist_ok=True)

                # Backup existing
                backup = Path(str(self._rules_dir) + ".bak")
                if self._rules_dir.exists():
                    if backup.exists():
                        shutil.rmtree(str(backup))
                    shutil.copytree(str(self._rules_dir), str(backup))

                # Copy new rules
                for f in rules_src.glob("*.conf"):
                    shutil.copy2(str(f), str(self._rules_dir / f.name))

                # Copy setup file if exists
                setup_src = extracted[0] / "crs-setup.conf.example"
                if setup_src.exists():
                    shutil.copy2(str(setup_src), str(self._rules_dir.parent / "crs-setup.conf.example"))

                return {
                    "success": True,
                    "version": tag,
                    "rules_count": len(list(self._rules_dir.glob("*.conf"))),
                }

            finally:
                shutil.rmtree(tmp_dir, ignore_errors=True)

        except Exception as exc:
            logger.exception("CRS update failed")
            return {"success": False, "error": str(exc)}

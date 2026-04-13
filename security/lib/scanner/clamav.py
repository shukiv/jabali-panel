"""Optional ClamAV backend — communicates with clamd via raw Unix socket.

clamd is NOT installed by default (it uses ~950MB RSS). The base `clamav`
package provides `clamscan` CLI and virus definitions. Admins who want
daemon-based scanning can install clamav-daemon themselves; this scanner
auto-detects the socket when present.
"""

from __future__ import annotations

import asyncio
import logging
import os

from lib.models import Finding

logger = logging.getLogger(__name__)


class ClamavScanner:
    name = "clamav"

    def __init__(self, socket_path: str = "/var/run/clamav/clamd.ctl", mode: str = "auto") -> None:
        """
        mode: "auto" (detect clamd socket), "yes" (require), "no" (disable)
        """
        if mode not in ("auto", "yes", "no"):
            logger.warning("Invalid ClamAV mode %r, falling back to 'auto'", mode)
            mode = "auto"
        self._socket_path = socket_path
        self._mode = mode
        self._available = False
        self._detect_availability()

    def _detect_availability(self) -> None:
        if self._mode == "no":
            self._available = False
            return
        exists = os.path.exists(self._socket_path)
        if self._mode == "yes" and not exists:
            logger.error("ClamAV required but clamd socket not found: %s", self._socket_path)
        if self._mode == "auto" and exists:
            logger.info("ClamAV detected at %s", self._socket_path)
        self._available = exists

    async def scan(self, path: str, content: bytes) -> list[Finding]:
        """Scan file via clamd Unix socket using zSCAN command."""
        if not self._available:
            return []

        # Validate path: reject null bytes to prevent protocol injection
        if "\x00" in path:
            logger.warning("Rejecting path with null byte for ClamAV scan")
            return []

        try:
            reader, writer = await asyncio.open_unix_connection(self._socket_path)
            try:
                # Use zSCAN (null-terminated) for scanning by path
                command = b"zSCAN " + path.encode("utf-8") + b"\x00"
                writer.write(command)
                await writer.drain()

                response = await asyncio.wait_for(reader.read(4096), timeout=30.0)
                result = response.rstrip(b"\x00").decode("utf-8", errors="replace").strip()

                # Response format: "/path/to/file: OK" or "/path/to/file: VirusName FOUND"
                # Parse strictly: split on last ":" and check suffix ends with " FOUND"
                parts = result.rsplit(":", 1)
                if len(parts) == 2:
                    status = parts[1].strip()
                    if status.endswith("FOUND"):
                        virus_name = status[:-5].strip()  # Remove trailing "FOUND"
                        if not virus_name:
                            virus_name = "unknown"
                        return [Finding(
                            scanner="clamav",
                            rule=virus_name,
                            score=35,
                            description="ClamAV detection: %s" % virus_name,
                            metadata={"path": path, "raw_response": result[:200]},
                        )]
            finally:
                writer.close()
                await writer.wait_closed()
        except asyncio.TimeoutError:
            logger.warning("ClamAV scan timed out for %s", path)
        except (ConnectionRefusedError, FileNotFoundError):
            logger.warning("ClamAV socket unavailable: %s", self._socket_path)
            self._available = False
        except Exception:
            logger.exception("ClamAV scan error for %s", path)

        return []

    @property
    def enabled(self) -> bool:
        return self._available

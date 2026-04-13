"""GeoIP country lookup and MaxMind database management."""

from __future__ import annotations

import asyncio
import ipaddress
import logging
import os
import re
import shutil
import tarfile
import tempfile
from pathlib import Path

import maxminddb

logger = logging.getLogger(__name__)

# SECURITY: Validate action parameter to prevent nginx directive injection
_VALID_ACTIONS = {"block", "challenge", "log"}

# SECURITY: Whitelist safe characters for nginx paths
_SAFE_PATH_RE = re.compile(r"^[a-zA-Z0-9/_.\-]+$")

_DOWNLOAD_URL = (
    "https://download.maxmind.com/app/geoip_download"
    "?edition_id={edition}&license_key={key}&suffix=tar.gz"
)


class GeoIPManager:
    """MaxMind GeoLite2/GeoIP2 country database manager."""

    def __init__(
        self,
        db_path: str = "/var/lib/jabali-security/GeoLite2-Country.mmdb",
        license_key: str = "",
        edition: str = "GeoLite2-Country",
    ) -> None:
        self._db_path = Path(db_path)
        self._license_key = license_key
        self._edition = edition
        self._reader: maxminddb.Reader | None = None

    def _open(self) -> maxminddb.Reader | None:
        """Lazy-load the database on first lookup."""
        if self._reader is not None:
            return self._reader
        if not self._db_path.is_file():
            return None
        try:
            self._reader = maxminddb.open_database(str(self._db_path))
            logger.info("GeoIP database loaded: %s", self._db_path)
            return self._reader
        except Exception:
            logger.exception("Failed to open GeoIP database: %s", self._db_path)
            return None

    def lookup(self, ip: str) -> str | None:
        """Return ISO 3166-1 alpha-2 country code for an IP, or None."""
        reader = self._open()
        if reader is None:
            return None
        try:
            ipaddress.ip_address(ip)
            result = reader.get(ip)
            if result and isinstance(result, dict):
                country = result.get("country", {})
                return country.get("iso_code")
        except (ValueError, maxminddb.InvalidDatabaseError):
            pass
        return None

    def is_available(self) -> bool:
        """Check if the database file exists."""
        return self._db_path.is_file()

    def db_info(self) -> dict:
        """Return database metadata."""
        if not self.is_available():
            return {"available": False, "path": str(self._db_path)}
        reader = self._open()
        meta: dict = {
            "available": True,
            "path": str(self._db_path),
            "size_bytes": self._db_path.stat().st_size,
            "modified": self._db_path.stat().st_mtime,
        }
        if reader:
            md = reader.metadata()
            meta["build_epoch"] = md.build_epoch
            meta["database_type"] = md.database_type
            meta["node_count"] = md.node_count
        return meta

    async def download_database(self) -> tuple[bool, str]:
        """Download GeoLite2-Country.mmdb from MaxMind. Returns (success, message)."""
        if not self._license_key:
            return False, "No MaxMind license key configured"

        url = _DOWNLOAD_URL.format(edition=self._edition, key=self._license_key)

        try:
            return await asyncio.to_thread(self._download_sync, url)
        except Exception as exc:
            msg = "GeoIP download failed: %s" % exc
            logger.exception(msg)
            return False, msg

    def _download_sync(self, url: str) -> tuple[bool, str]:
        """Synchronous download + extract (runs in executor)."""
        import urllib.request

        tmp_dir = tempfile.mkdtemp(prefix="jabali-geoip-")
        tar_path = os.path.join(tmp_dir, "geoip.tar.gz")

        try:
            req = urllib.request.Request(url)  # noqa: S310
            with urllib.request.urlopen(req, timeout=60) as resp:  # noqa: S310
                with open(tar_path, "wb") as f:
                    shutil.copyfileobj(resp, f)

            with tarfile.open(tar_path, "r:gz") as tar:
                for member in tar.getmembers():
                    if member.name.endswith(".mmdb"):
                        member_file = tar.extractfile(member)
                        if member_file:
                            self._db_path.parent.mkdir(parents=True, exist_ok=True)
                            with open(self._db_path, "wb") as out:
                                shutil.copyfileobj(member_file, out)
                            # Reload reader
                            if self._reader:
                                self._reader.close()
                                self._reader = None
                            logger.info("GeoIP database updated: %s", self._db_path)
                            return True, "Database updated: %s" % self._db_path
            return False, "No .mmdb file found in archive"
        finally:
            shutil.rmtree(tmp_dir, ignore_errors=True)

    def check_ip(self, ip: str, blocked_countries: list[str], allowed_countries: list[str]) -> str:
        """Check if an IP should be blocked/allowed based on country rules.

        Returns: "block", "allow", or "pass"
        - If allowed_countries is set (whitelist mode): block everything NOT in the list
        - If blocked_countries is set (blocklist mode): block IPs from those countries
        - Otherwise: pass
        """
        country = self.lookup(ip)
        if not country:
            return "pass"

        if allowed_countries:
            return "pass" if country in allowed_countries else "block"
        if blocked_countries:
            return "block" if country in blocked_countries else "pass"
        return "pass"

    def write_nginx_configs(
        self,
        blocked_countries: list[str],
        allowed_countries: list[str],
        action: str = "block",
    ) -> list[str]:
        """Write standalone nginx GeoIP configs. Returns list of paths written.

        Writes two files:
        - /etc/nginx/jabali/cache-zones/geoip.conf  (http-level: geoip2 + map)
        - /etc/nginx/jabali/includes/geo.conf        (server-level: if blocks)
        """
        # SECURITY: Validate action before using in config
        if action not in _VALID_ACTIONS:
            raise ValueError(f"Invalid action: {action} (must be one of {_VALID_ACTIONS})")

        http_dir = Path("/etc/nginx/jabali/cache-zones")
        server_dir = Path("/etc/nginx/jabali/includes")
        written: list[str] = []

        # --- http-level config: geoip2 directive + country map ---
        http_lines = [
            "# Jabali Security GeoIP -- auto-generated",
            "# Included in nginx http{} block",
            "",
            "# Challenge cookie bypass — check if jabali_passed cookie exists",
            "map $cookie_jabali_passed $jabali_challenge_valid {",
            "    default '0';",
            "    '~.+' '1';",
            "}",
        ]

        if not self._db_path.is_file():
            # No database — write empty passthrough
            http_lines += [
                "# GeoIP database not available",
                "map $remote_addr $jabali_geo_action {",
                "    default 'pass';",
                "}",
            ]
        else:
            # SECURITY: Validate db_path contains only safe characters
            db_path_str = str(self._db_path)
            if not _SAFE_PATH_RE.match(db_path_str) or any(
                char in db_path_str for char in ["'", '"', ";", "{", "}", "$", "`"]
            ):
                logger.error("Invalid characters in GeoIP db_path: %s", db_path_str)
                # Fall back to empty passthrough config (GeoIP disabled)
                http_lines += [
                    "",
                    "# GeoIP database path contains invalid characters (security measure)",
                    "map $remote_addr $jabali_geo_action {",
                    "    default 'pass';",
                    "}",
                    "",
                ]
            else:
                http_lines += [
                    "",
                    "geoip2 %s {" % db_path_str,
                    "    auto_reload 60m;",
                    "    $geoip2_country_code country iso_code;",
                    "}",
                    "",
                ]

            if allowed_countries:
                http_lines.append("# Whitelist mode: block all except listed")
                http_lines.append("map $geoip2_country_code $jabali_geo_action {")
                http_lines.append("    default '%s';" % action)
                for cc in allowed_countries:
                    safe_cc = cc.upper()[:2]
                    http_lines.append("    %s 'pass';" % safe_cc)
                http_lines.append("}")
            elif blocked_countries:
                http_lines.append("# Blocklist mode: allow all except listed")
                http_lines.append("map $geoip2_country_code $jabali_geo_action {")
                http_lines.append("    default 'pass';")
                for cc in blocked_countries:
                    safe_cc = cc.upper()[:2]
                    http_lines.append("    %s '%s';" % (safe_cc, action))
                http_lines.append("}")
            else:
                http_lines.append("map $geoip2_country_code $jabali_geo_action {")
                http_lines.append("    default 'pass';")
                http_lines.append("}")

        http_path = http_dir / "geoip.conf"
        http_dir.mkdir(parents=True, exist_ok=True)
        http_path.write_text("\n".join(http_lines) + "\n", encoding="utf-8")
        written.append(str(http_path))

        # --- server-level config: if blocks with cookie bypass ---
        server_lines = [
            "# Jabali Security GeoIP -- auto-generated",
            "# Included in nginx server{} blocks",
            "",
            "if ($jabali_geo_action = 'block') {",
            "    return 403;",
            "}",
            "",
            "# Challenge: bypass if PoW cookie is valid",
            "set $jabali_do_geo_challenge '';",
            "if ($jabali_geo_action = 'challenge') {",
            "    set $jabali_do_geo_challenge 'yes';",
            "}",
            "if ($jabali_challenge_valid = '1') {",
            "    set $jabali_do_geo_challenge '';",
            "}",
            "if ($jabali_do_geo_challenge = 'yes') {",
            "    rewrite ^ /jabali-challenge.html last;",
            "}",
            "",
            "# Challenge page (served from filesystem, not proxied)",
            "location = /jabali-challenge.html {",
            "    root /etc/nginx/jabali/challenge;",
            "    default_type text/html;",
            "}",
        ]

        server_path = server_dir / "geo.conf"
        server_dir.mkdir(parents=True, exist_ok=True)
        server_path.write_text("\n".join(server_lines) + "\n", encoding="utf-8")
        written.append(str(server_path))

        # Deploy challenge page and njs script
        challenge_dir = Path("/etc/nginx/jabali/challenge")
        challenge_dir.mkdir(parents=True, exist_ok=True)
        njs_dir = Path("/etc/nginx/jabali-security")
        njs_dir.mkdir(parents=True, exist_ok=True)

        src_dir = Path(__file__).parent.parent.parent / "etc" / "webshield"
        challenge_src = src_dir / "challenge.html"
        njs_src = src_dir / "jabali_challenge.js"

        if challenge_src.is_file():
            shutil.copy2(str(challenge_src), str(challenge_dir / "jabali-challenge.html"))
            written.append(str(challenge_dir / "jabali-challenge.html"))
        if njs_src.is_file():
            shutil.copy2(str(njs_src), str(njs_dir / "jabali_challenge.js"))
            written.append(str(njs_dir / "jabali_challenge.js"))

        # Reload nginx
        import subprocess

        from lib.privilege import sudo_cmd
        try:
            subprocess.run(  # noqa: S603
                sudo_cmd("/usr/sbin/nginx", "-t"),
                capture_output=True, timeout=10,
            )
            subprocess.run(  # noqa: S603
                sudo_cmd("/usr/sbin/nginx", "-s", "reload"),
                capture_output=True, timeout=10,
            )
            logger.info("Nginx reloaded with GeoIP config")
        except Exception:
            logger.warning("Failed to reload nginx after GeoIP config update")

        return written

    def close(self) -> None:
        """Close the database reader."""
        if self._reader:
            self._reader.close()
            self._reader = None

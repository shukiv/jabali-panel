"""WebShield manager -- install, uninstall, and manage nginx bot filtering."""
from __future__ import annotations

import asyncio
import logging
import os
import re
import shutil
from datetime import datetime, timedelta, timezone
from pathlib import Path

from lib.webshield.bot_rules import get_rules
from lib.webshield.config_generator import NginxConfigGenerator
from lib.webshield.models import WebShieldStatus

logger = logging.getLogger(__name__)

_NGINX_DATE_RE = re.compile(r'\[(\d{2}/\w{3}/\d{4}:\d{2}:\d{2}:\d{2}) [+\-]\d{4}\]')
_NGINX_STATUS_RE = re.compile(r'" (\d{3}) ')


class WebShieldManager:
    """Manage WebShield nginx configuration."""

    def __init__(
        self,
        config_dir: str = "/etc/nginx/jabali-security",
        rate_limiting: bool = False,
        rate_limit: int = 10,
        rate_burst: int = 20,
        challenge_enabled: bool = True,
        bot_filtering: bool = True,
        geoip_enabled: bool = False,
        geoip_db_path: str = "",
        geoip_blocked_countries: list[str] | None = None,
        geoip_allowed_countries: list[str] | None = None,
        geoip_action: str = "block",
    ) -> None:
        self._config_dir = Path(config_dir)
        self._rate_limiting = rate_limiting
        self._rate_limit = rate_limit
        self._rate_burst = rate_burst
        self._challenge_enabled = challenge_enabled
        self._bot_filtering = bot_filtering
        self._geoip_enabled = geoip_enabled
        self._geoip_db_path = geoip_db_path
        self._generator = NginxConfigGenerator(
            config_dir=config_dir,
            rate_limit=rate_limit,
            rate_burst=rate_burst,
            rate_limiting=rate_limiting,
            geoip_enabled=geoip_enabled,
            geoip_db_path=geoip_db_path,
            geoip_blocked_countries=geoip_blocked_countries,
            geoip_allowed_countries=geoip_allowed_countries,
            geoip_action=geoip_action,
        )

    async def install(self) -> dict:
        """Install WebShield nginx configs. Returns status dict."""
        if not shutil.which("nginx"):
            return {"success": False, "error": "nginx not found"}

        try:
            written = self._generator.write_configs()

            # Deploy shared challenge page and njs script
            src_dir = Path(__file__).parent.parent.parent / "etc" / "webshield"
            challenge_dir = Path("/etc/nginx/jabali/challenge")
            challenge_dir.mkdir(parents=True, exist_ok=True)
            njs_dir = Path("/etc/nginx/jabali-security")
            njs_dir.mkdir(parents=True, exist_ok=True)

            challenge_src = src_dir / "challenge.html"
            if challenge_src.is_file():
                shutil.copy2(str(challenge_src), str(challenge_dir / "jabali-challenge.html"))
                written.append(str(challenge_dir / "jabali-challenge.html"))
            njs_src = src_dir / "jabali_challenge.js"
            if njs_src.is_file():
                shutil.copy2(str(njs_src), str(njs_dir / "jabali_challenge.js"))
                written.append(str(njs_dir / "jabali_challenge.js"))

            # Add http-level include to nginx.conf if not present
            self._add_nginx_http_include()

            # Test nginx config
            test_ok = await self._test_nginx_config()

            return {
                "success": True,
                "files_written": written,
                "nginx_config_valid": test_ok,
            }
        except OSError as exc:
            return {"success": False, "error": str(exc)}

    async def uninstall(self) -> dict:
        """Remove WebShield nginx configs."""
        removed: list[str] = []
        for name in ("jabali-webshield-http.conf", "jabali-webshield-server.conf",
                      "jabali-challenge.html", "blocked_ips.conf"):
            p = self._config_dir / name
            if p.is_file():
                p.unlink()
                removed.append(str(p))

        # Remove http-level include from nginx.conf
        self._remove_nginx_http_include()

        return {"success": True, "files_removed": removed}

    async def update_blocked_ips(self, ips: list[str]) -> bool:
        """Write blocked IPs to nginx include file."""
        try:
            content = self._generator.generate_blocked_ips_conf(ips)
            (self._config_dir / "blocked_ips.conf").write_text(content, encoding="utf-8")
            return True
        except OSError:
            return False

    def get_status(self, access_log: str = "/var/log/nginx/access.log") -> WebShieldStatus:
        """Get current WebShield status."""
        http_conf = self._config_dir / "jabali-webshield-http.conf"
        server_conf = self._config_dir / "jabali-webshield-server.conf"
        blocked_conf = self._config_dir / "blocked_ips.conf"

        blocked_count = 0
        if blocked_conf.is_file():
            blocked_count = sum(1 for line in blocked_conf.read_text().splitlines()
                                if line.strip() and not line.startswith("#"))

        counts = self._count_blocked_requests(access_log)

        return WebShieldStatus(
            installed=http_conf.is_file() and server_conf.is_file(),
            nginx_available=shutil.which("nginx") is not None,
            rate_limiting=self._rate_limiting and server_conf.is_file(),
            bot_filtering=http_conf.is_file(),
            challenge_enabled=self._challenge_enabled,
            blocked_ips_count=blocked_count,
            config_dir=str(self._config_dir),
            bot_blocked_24h=counts["bot_blocked"],
            rate_limited_24h=counts["rate_limited"],
            challenged_24h=counts["challenged"],
        )

    def _count_blocked_requests(self, access_log: str = "/var/log/nginx/access.log") -> dict[str, int]:
        """Count blocked/challenged requests from nginx access log in last 24h."""
        counts = {"bot_blocked": 0, "rate_limited": 0, "challenged": 0}
        log_path = Path(access_log)
        if not log_path.is_file():
            return counts

        cutoff = datetime.now(timezone.utc) - timedelta(hours=24)
        try:
            for line in log_path.read_text(errors="replace").splitlines():
                dm = _NGINX_DATE_RE.search(line)
                if not dm:
                    continue
                try:
                    ts = datetime.strptime(dm.group(1), "%d/%b/%Y:%H:%M:%S").replace(tzinfo=timezone.utc)
                except ValueError:
                    continue
                if ts < cutoff:
                    continue

                sm = _NGINX_STATUS_RE.search(line)
                if not sm:
                    continue
                status = sm.group(1)
                if status == "403":
                    counts["bot_blocked"] += 1
                elif status == "429":
                    counts["rate_limited"] += 1
                elif status == "503":
                    counts["challenged"] += 1
        except OSError:
            pass
        return counts

    def get_rules(self) -> list[dict]:
        """Get bot rules as dicts."""
        return [r.model_dump() for r in get_rules()]

    _NGINX_CONF = Path("/etc/nginx/nginx.conf")

    def _add_nginx_http_include(self) -> None:
        """Add WebShield http-level include to nginx.conf if not present."""
        include_line = "\tinclude %s/jabali-webshield-http.conf;" % self._config_dir
        if not self._NGINX_CONF.is_file():
            return
        content = self._NGINX_CONF.read_text()
        if "jabali-webshield-http.conf" in content:
            return
        # Insert after 'modsecurity on;' or after 'http {' line
        for marker in ("modsecurity on;", "http {"):
            if marker in content:
                content = content.replace(marker, marker + "\n" + include_line, 1)
                # Atomic write to prevent corruption on crash
                import tempfile
                fd, tmp = tempfile.mkstemp(dir=str(self._NGINX_CONF.parent), prefix=".nginx.jabali.")
                try:
                    os.write(fd, content.encode())
                    os.close(fd)
                    os.chmod(tmp, 0o644)
                    os.rename(tmp, str(self._NGINX_CONF))
                except OSError:
                    os.close(fd) if not os.get_inheritable(fd) else None
                    try:
                        os.unlink(tmp)
                    except OSError:
                        pass
                    raise
                logger.info("Added WebShield http include to nginx.conf")
                return

    def _remove_nginx_http_include(self) -> None:
        """Remove WebShield http-level include from nginx.conf."""
        if not self._NGINX_CONF.is_file():
            return
        lines = self._NGINX_CONF.read_text().splitlines()
        filtered = [line for line in lines if "jabali-webshield-http.conf" not in line]
        if len(filtered) < len(lines):
            self._NGINX_CONF.write_text("\n".join(filtered) + "\n")
            logger.info("Removed WebShield http include from nginx.conf")

    @staticmethod
    async def _test_nginx_config() -> bool:
        """Test nginx configuration. Returns True if valid."""
        from lib.privilege import sudo_prefix
        cmd = [*sudo_prefix(), "nginx", "-t"]
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
            )
            await proc.communicate()
            return proc.returncode == 0
        except OSError:
            return False


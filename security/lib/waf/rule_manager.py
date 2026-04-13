"""ModSecurity rule manager -- enable/disable rules via override config."""
from __future__ import annotations

import asyncio
import logging
import re
import shutil
from pathlib import Path

logger = logging.getLogger(__name__)


class WafRuleManager:
    """Manage ModSecurity rules without editing CRS files directly."""

    def __init__(self, overrides_file: str, rules_dir: str, web_server: str = "auto",
                 nginx_include: str = "") -> None:
        self._overrides = Path(overrides_file)
        self._rules_dir = Path(rules_dir)
        self._web_server = self._detect_web_server(web_server)
        self._nginx_include = Path(nginx_include) if nginx_include else None
        self._disabled_rules: set[int] = set()
        self._load_overrides()

    @staticmethod
    def _detect_web_server(preference: str) -> str:
        if preference != "auto":
            return preference
        if shutil.which("nginx"):
            return "nginx"
        if shutil.which("apache2ctl") or shutil.which("apachectl"):
            return "apache"
        return "unknown"

    def _load_overrides(self) -> None:
        """Load currently disabled rule IDs from overrides file."""
        if not self._overrides.is_file():
            return
        for line in self._overrides.read_text().splitlines():
            m = re.match(r"SecRuleRemoveById\s+(\d+)", line.strip())
            if m:
                self._disabled_rules.add(int(m.group(1)))

    def _save_overrides(self) -> None:
        """Write overrides file with disabled rules."""
        self._overrides.parent.mkdir(parents=True, exist_ok=True)
        lines = [
            "# Jabali Security -- ModSecurity rule overrides",
            "# Auto-generated. Do not edit manually.",
            "",
        ]
        for rule_id in sorted(self._disabled_rules):
            lines.append("SecRuleRemoveById %d" % rule_id)
        self._overrides.write_text("\n".join(lines) + "\n")

    async def disable_rule(self, rule_id: int) -> bool:
        self._disabled_rules.add(rule_id)
        self._save_overrides()
        return await self._reload_web_server()

    async def enable_rule(self, rule_id: int) -> bool:
        self._disabled_rules.discard(rule_id)
        self._save_overrides()
        return await self._reload_web_server()

    def list_disabled(self) -> list[int]:
        return sorted(self._disabled_rules)

    async def list_rules(self) -> list[dict]:
        """List available CRS rule files."""
        if not self._rules_dir.is_dir():
            return []
        rules = []
        for f in sorted(self._rules_dir.glob("*.conf")):
            rules.append({
                "file": f.name,
                "path": str(f),
                "size": f.stat().st_size,
            })
        return rules

    _WEBSHIELD_SERVER_CONF = "/etc/nginx/jabali-security/jabali-webshield-server.conf"

    async def set_modsecurity_enabled(self, enabled: bool) -> bool:
        """Write modsecurity on/off to the nginx include file and reload."""
        if not self._nginx_include:
            logger.warning("No WAF_NGINX_INCLUDE configured")
            return False
        self._nginx_include.parent.mkdir(parents=True, exist_ok=True)
        state = "on" if enabled else "off"
        self._nginx_include.write_text(self._build_waf_conf(state))
        logger.info("Set modsecurity %s in %s", state, self._nginx_include)
        return await self._reload_web_server()

    def _build_waf_conf(self, modsec_state: str) -> str:
        """Build the waf.conf include content with all hardening blocks."""
        lines = ["# Managed by Jabali Security", "modsecurity %s;" % modsec_state, ""]

        # Security headers
        lines += [
            "# Security headers",
            'add_header X-Frame-Options "SAMEORIGIN" always;',
            'add_header X-Content-Type-Options "nosniff" always;',
            'add_header X-XSS-Protection "1; mode=block" always;',
            'add_header Referrer-Policy "strict-origin-when-cross-origin" always;',
            "add_header Content-Security-Policy \"default-src 'self' https: data:"
            " 'unsafe-inline' 'unsafe-eval'\" always;",
            "",
        ]

        # XML-RPC block
        lines += [
            "# Block XML-RPC (WordPress brute-force vector)",
            "location = /xmlrpc.php {",
            "    deny all;",
            "    return 403;",
            "}",
            "",
        ]

        # WebShield server-level config (rate limiting + bot blocking)
        ws_conf = Path(self._WEBSHIELD_SERVER_CONF)
        if ws_conf.is_file():
            lines += [
                "# WebShield server-level protection",
                "include %s;" % ws_conf,
            ]

        return "\n".join(lines) + "\n"

    def is_modsecurity_enabled(self) -> bool:
        """Check if modsecurity is on in the nginx include file."""
        if not self._nginx_include or not self._nginx_include.is_file():
            return False
        content = self._nginx_include.read_text()
        return "modsecurity on" in content.lower().replace(" ", " ")

    async def _reload_web_server(self) -> bool:
        """Reload nginx or apache to apply rule changes."""
        if self._web_server == "nginx":
            return await self._run_cmd("nginx", "-s", "reload")
        elif self._web_server == "apache":
            cmd = "apache2ctl" if shutil.which("apache2ctl") else "apachectl"
            return await self._run_cmd(cmd, "graceful")
        return False

    @staticmethod
    async def _run_cmd(*args: str) -> bool:
        from lib.privilege import sudo_prefix
        cmd = [*sudo_prefix(), *args]
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
            )
            await proc.communicate()
            return proc.returncode == 0
        except OSError:
            return False

    @property
    def web_server(self) -> str:
        return self._web_server

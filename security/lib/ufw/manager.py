"""UFW firewall rule manager."""
from __future__ import annotations

import asyncio
import logging
import re
import shutil

from lib.ufw.models import UfwAppProfile, UfwRule, UfwStatus

logger = logging.getLogger(__name__)

_STATUS_RE = re.compile(r"^Status:\s+(active|inactive)", re.MULTILINE)
_DEFAULT_RE = re.compile(
    r"Default:\s+(\w+)\s+\(incoming\),\s+(\w+)\s+\(outgoing\),\s+(\w+)\s+\(routed\)"
)
_RULE_RE = re.compile(
    r"^\[\s*(\d+)\]\s+(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)\s+(IN|OUT|FWD)?\s*(.*)"
)


class UFWManager:
    def __init__(self) -> None:
        self._available: bool | None = None
        self._lock = asyncio.Lock()

    @property
    def available(self) -> bool:
        if self._available is None:
            self._available = shutil.which("ufw") is not None
        return self._available

    async def get_status(self) -> UfwStatus:
        """Get UFW status including defaults and rules."""
        if not self.available:
            return UfwStatus(available=False)

        rc, stdout, stderr = await self._run("ufw", "status", "verbose")
        if rc != 0:
            logger.warning("ufw status verbose failed: %s", stderr)
            return UfwStatus(available=True)

        active = False
        m = _STATUS_RE.search(stdout)
        if m:
            active = m.group(1) == "active"

        default_incoming = ""
        default_outgoing = ""
        default_routed = ""
        m = _DEFAULT_RE.search(stdout)
        if m:
            default_incoming = m.group(1)
            default_outgoing = m.group(2)
            default_routed = m.group(3)

        rules = await self.list_rules()

        return UfwStatus(
            available=True,
            active=active,
            default_incoming=default_incoming,
            default_outgoing=default_outgoing,
            default_routed=default_routed,
            rules=rules,
            rules_count=len(rules),
        )

    async def list_rules(self) -> list[UfwRule]:
        """List all numbered UFW rules."""
        if not self.available:
            return []

        rc, stdout, stderr = await self._run("ufw", "status", "numbered")
        if rc != 0:
            logger.warning("ufw status numbered failed: %s", stderr)
            return []

        rules: list[UfwRule] = []
        for line in stdout.splitlines():
            m = _RULE_RE.match(line)
            if not m:
                continue

            number = int(m.group(1))
            to_field = m.group(2).strip()
            action = m.group(3).strip()
            direction = (m.group(4) or "").strip()
            from_field = m.group(5).strip()

            v6 = False
            if to_field.endswith("(v6)"):
                to_field = to_field[:-4].strip()
                v6 = True
            if from_field.endswith("(v6)"):
                from_field = from_field[:-4].strip()
                v6 = True

            rules.append(UfwRule(
                number=number,
                to=to_field,
                action=action,
                direction=direction,
                v6=v6,
                raw=line.strip(),
                **{"from": from_field},
            ))

        return rules

    async def add_rule(
        self,
        *,
        action: str,
        port: str | None = None,
        protocol: str | None = None,
        from_ip: str | None = None,
        to_ip: str | None = None,
        direction: str | None = None,
        comment: str | None = None,
    ) -> tuple[bool, str]:
        """Add a UFW rule. Returns (success, output_message)."""
        args: list[str] = ["ufw"]

        if direction:
            args.append(direction)

        args.append(action)

        if from_ip:
            args.extend(["from", from_ip])

        if port or to_ip:
            args.extend(["to", to_ip or "any"])

        if port:
            args.extend(["port", port])

        if protocol and protocol != "any":
            args.extend(["proto", protocol])

        if comment:
            args.extend(["comment", comment])

        async with self._lock:
            rc, stdout, stderr = await self._run(*args)
        output = stdout.strip() or stderr.strip()
        return (rc == 0, output)

    async def remove_rule(self, number: int) -> tuple[bool, str]:
        """Remove a UFW rule by number."""
        async with self._lock:
            rc, stdout, stderr = await self._run("ufw", "--force", "delete", str(number))
        output = stdout.strip() or stderr.strip()
        return (rc == 0, output)

    async def enable(self) -> tuple[bool, str]:
        """Enable UFW."""
        async with self._lock:
            rc, stdout, stderr = await self._run("ufw", "--force", "enable")
        output = stdout.strip() or stderr.strip()
        return (rc == 0, output)

    async def disable(self) -> tuple[bool, str]:
        """Disable UFW."""
        async with self._lock:
            rc, stdout, stderr = await self._run("ufw", "--force", "disable")
        output = stdout.strip() or stderr.strip()
        return (rc == 0, output)

    async def reload(self) -> tuple[bool, str]:
        """Reload UFW."""
        async with self._lock:
            rc, stdout, stderr = await self._run("ufw", "reload")
        output = stdout.strip() or stderr.strip()
        return (rc == 0, output)

    async def list_app_profiles(self) -> list[str]:
        """List available UFW application profiles."""
        if not self.available:
            return []

        rc, stdout, stderr = await self._run("ufw", "app", "list")
        if rc != 0:
            logger.warning("ufw app list failed: %s", stderr)
            return []

        profiles: list[str] = []
        past_header = False
        for line in stdout.splitlines():
            if line.startswith("Available applications:"):
                past_header = True
                continue
            if past_header:
                name = line.strip()
                if name:
                    profiles.append(name)
        return profiles

    async def get_app_info(self, name: str) -> UfwAppProfile | None:
        """Get info for a UFW application profile."""
        if not self.available:
            return None

        rc, stdout, stderr = await self._run("ufw", "app", "info", name)
        if rc != 0:
            logger.warning("ufw app info %r failed: %s", name, stderr)
            return None

        fields: dict[str, str] = {}
        for line in stdout.splitlines():
            if ":" in line:
                key, _, value = line.partition(":")
                fields[key.strip()] = value.strip()

        profile_name = fields.get("Profile", name)
        return UfwAppProfile(
            name=profile_name,
            title=fields.get("Title", ""),
            description=fields.get("Description", ""),
            ports=fields.get("Ports", ""),
        )

    async def allow_app(self, name: str) -> tuple[bool, str]:
        """Allow a UFW application profile."""
        async with self._lock:
            rc, stdout, stderr = await self._run("ufw", "allow", name)
        output = stdout.strip() or stderr.strip()
        return (rc == 0, output)

    async def deny_app(self, name: str) -> tuple[bool, str]:
        """Deny a UFW application profile."""
        async with self._lock:
            rc, stdout, stderr = await self._run("ufw", "deny", name)
        output = stdout.strip() or stderr.strip()
        return (rc == 0, output)

    @staticmethod
    async def _run(*args: str) -> tuple[int, str, str]:
        """Run a command, return (exit_code, stdout, stderr). Never uses shell.

        Note: stdout/stderr may contain internal system details and must
        never be returned directly to API clients.
        """
        from lib.privilege import sudo_prefix
        cmd = [*sudo_prefix(), *args]
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=30)
            return (
                proc.returncode or 0,
                stdout.decode(errors="replace"),
                stderr.decode(errors="replace"),
            )
        except asyncio.TimeoutError:
            logger.error("UFW command timed out: %s", args[0] if args else "ufw")
            proc.kill()
            await proc.wait()
            return (1, "", "Command timed out")
        except OSError as exc:
            logger.error("UFW command failed: %s -- %s", args[0] if args else "ufw", exc)
            return (1, "", "Command execution failed")

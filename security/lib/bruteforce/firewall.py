"""Firewall manager -- nftables/iptables abstraction for IP blocking."""
from __future__ import annotations

import asyncio
import ipaddress
import logging
import re
import shutil

logger = logging.getLogger(__name__)


def _validate_ip(ip: str) -> bool:
    """Validate an IP address string (IPv4 or IPv6)."""
    try:
        ipaddress.ip_address(ip)
        return True
    except ValueError:
        return False


class FirewallManager:
    """Manage IP blocks via nftables or iptables."""

    def __init__(self, backend: str = "auto") -> None:
        """backend: 'auto', 'nftables', 'iptables', 'none'"""
        self._backend = self._detect_backend(backend)
        self._initialized = False
        logger.info("Firewall backend: %s", self._backend)

    @staticmethod
    def _detect_backend(preference: str) -> str:
        if preference != "auto":
            return preference
        if shutil.which("nft"):
            return "nftables"
        if shutil.which("iptables"):
            return "iptables"
        logger.warning("No firewall backend found (nft/iptables)")
        return "none"

    async def initialize(self) -> None:
        """Set up the jabali-security chain/set in the firewall."""
        if self._backend == "none":
            return
        if self._backend == "nftables":
            await self._nft_init()
        elif self._backend == "iptables":
            await self._ipt_init()
        self._initialized = True

    async def block_ip(self, ip: str, duration: int = 0) -> bool:
        """Block an IP. duration=0 means permanent. Returns success."""
        if not _validate_ip(ip):
            logger.warning("Refusing to block invalid IP: %r", ip)
            return False
        if self._backend == "none":
            return False
        if self._backend == "nftables":
            return await self._nft_block(ip, duration)
        if self._backend == "iptables":
            return await self._ipt_block(ip)
        return False

    async def unblock_ip(self, ip: str) -> bool:
        """Unblock an IP. Returns success."""
        if not _validate_ip(ip):
            return False
        if self._backend == "none":
            return False
        if self._backend == "nftables":
            return await self._nft_unblock(ip)
        if self._backend == "iptables":
            return await self._ipt_unblock(ip)
        return False

    # -- nftables implementation --

    async def _nft_init(self) -> None:
        """Create jabali-security table and set if not exists."""
        # Create table
        await self._run("nft", "add", "table", "inet", "jabali-security")
        # Create set for blocked IPv4 (spec passed as single arg)
        await self._run(
            "nft", "add", "set", "inet", "jabali-security", "blocked-v4",
            "{ type ipv4_addr; flags timeout; }",
        )
        # Create set for blocked IPv6
        await self._run(
            "nft", "add", "set", "inet", "jabali-security", "blocked-v6",
            "{ type ipv6_addr; flags timeout; }",
        )
        # Create chain with drop rule
        await self._run(
            "nft", "add", "chain", "inet", "jabali-security", "input",
            "{ type filter hook input priority -10; policy accept; }",
        )
        await self._run(
            "nft", "add", "rule", "inet", "jabali-security", "input",
            "ip", "saddr", "@blocked-v4", "drop",
        )
        await self._run(
            "nft", "add", "rule", "inet", "jabali-security", "input",
            "ip6", "saddr", "@blocked-v6", "drop",
        )

    async def _nft_block(self, ip: str, duration: int = 0) -> bool:
        set_name = "blocked-v6" if ":" in ip else "blocked-v4"
        if duration > 0:
            rc = await self._run(
                "nft", "add", "element", "inet", "jabali-security", set_name,
                "{", "%s timeout %ds" % (ip, duration), "}",
            )
        else:
            rc = await self._run(
                "nft", "add", "element", "inet", "jabali-security", set_name,
                "{", ip, "}",
            )
        return rc == 0

    async def _nft_unblock(self, ip: str) -> bool:
        set_name = "blocked-v6" if ":" in ip else "blocked-v4"
        rc = await self._run(
            "nft", "delete", "element", "inet", "jabali-security", set_name,
            "{", ip, "}",
        )
        return rc == 0

    async def _nft_list(self) -> list[str]:
        """List IPs in the blocked sets."""
        from lib.privilege import sudo_prefix
        ips: list[str] = []
        for set_name in ("blocked-v4", "blocked-v6"):
            proc = await asyncio.create_subprocess_exec(
                *sudo_prefix(), "nft", "list", "set", "inet", "jabali-security", set_name,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, _ = await proc.communicate()
            if stdout:
                # Parse "elements = { 1.2.3.4 timeout 599s, ... }"
                for m in re.finditer(
                    r"(\d+\.\d+\.\d+\.\d+|[0-9a-f:]+(?::[0-9a-f]+)+)", stdout.decode()
                ):
                    candidate = m.group(1)
                    if _validate_ip(candidate):
                        ips.append(candidate)
        return ips

    # -- iptables implementation --

    async def _ipt_init(self) -> None:
        """Create JABALI-SECURITY chain if not exists."""
        # Create chain (ignore error if already exists)
        await self._run("iptables", "-N", "JABALI-SECURITY")
        # Check if jump rule already exists
        rc = await self._run("iptables", "-C", "INPUT", "-j", "JABALI-SECURITY")
        if rc != 0:
            # Rule doesn't exist -- add it
            await self._run("iptables", "-I", "INPUT", "1", "-j", "JABALI-SECURITY")

    async def _ipt_block(self, ip: str) -> bool:
        # Check if already blocked
        rc = await self._run("iptables", "-C", "JABALI-SECURITY", "-s", ip, "-j", "DROP")
        if rc == 0:
            return True  # Already blocked
        rc = await self._run("iptables", "-A", "JABALI-SECURITY", "-s", ip, "-j", "DROP")
        return rc == 0

    async def _ipt_unblock(self, ip: str) -> bool:
        rc = await self._run("iptables", "-D", "JABALI-SECURITY", "-s", ip, "-j", "DROP")
        return rc == 0

    async def _ipt_list(self) -> list[str]:
        from lib.privilege import sudo_prefix
        proc = await asyncio.create_subprocess_exec(
            *sudo_prefix(), "iptables", "-L", "JABALI-SECURITY", "-n", "--line-numbers",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, _ = await proc.communicate()
        ips: list[str] = []
        if stdout:
            for line in stdout.decode().splitlines():
                m = re.match(r"\d+\s+DROP\s+\S+\s+--\s+(\S+)", line)
                if m and _validate_ip(m.group(1)):
                    ips.append(m.group(1))
        return ips

    # -- helper --

    @staticmethod
    async def _run(*args: str) -> int:
        """Run a command, return exit code. Uses list args, never shell."""
        from lib.privilege import sudo_prefix
        cmd = [*sudo_prefix(), *args]
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            _stdout, stderr = await proc.communicate()
            if proc.returncode != 0 and stderr:
                logger.warning("Firewall command failed: %s -- %s", " ".join(args), stderr.decode()[:200])
            return proc.returncode or 0
        except OSError as exc:
            logger.error("Firewall command failed: %s -- %s", " ".join(args), exc)
            return 1

    @property
    def backend(self) -> str:
        return self._backend

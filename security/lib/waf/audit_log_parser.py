"""ModSecurity audit log parser."""
from __future__ import annotations

import asyncio
import logging
import re
from pathlib import Path

from lib.log_tailer import AsyncLogTailer
from lib.tenant import resolve_user
from lib.waf.models import WafEvent

logger = logging.getLogger(__name__)

# Parse section A: [date] unique_id source_ip source_port dest_ip dest_port
_SECTION_A_RE = re.compile(
    r"\[(?P<timestamp>[^\]]+)\]\s+(?P<unique_id>\S+)\s+"
    r"(?P<src_ip>\d+\.\d+\.\d+\.\d+)\s+\d+\s+"
    r"(?P<dst_ip>\d+\.\d+\.\d+\.\d+)"
)

# Parse section B first line: METHOD URI PROTOCOL
_REQUEST_LINE_RE = re.compile(r"^(?P<method>[A-Z]+)\s+(?P<uri>\S+)\s+HTTP/")

# Parse H section messages: [id "NNNNN"] [msg "..."] [severity "..."]
_RULE_ID_RE = re.compile(r'\[id "(\d+)"\]')
_RULE_MSG_RE = re.compile(r'\[msg "([^"]+)"\]')
_SEVERITY_RE = re.compile(r'\[severity "([^"]+)"\]')
_ACTION_RE = re.compile(r"^Action:\s*(\S+)")
_ACCESS_DENIED_RE = re.compile(r"Access denied with code (\d+)")

# Section boundary: supports both --ID-LETTER-- (v2) and ---ID---LETTER-- (v3)
_SECTION_RE = re.compile(r"^-{2,3}[a-zA-Z0-9]+-{1,3}([A-Z])-{2,3}$")


class ModSecAuditLogParser:
    """Parse and tail ModSecurity serial audit log."""

    def __init__(self, log_path: str, log_type: str = "serial") -> None:
        self._log_path = log_path
        self._log_type = log_type
        self._running = False
        self._tailer: AsyncLogTailer | None = None

    async def run(self, callback) -> None:
        """Tail the audit log and emit WafEvent for each entry."""
        self._running = True
        p = Path(self._log_path)

        if not p.exists():
            logger.warning("WAF audit log not found: %s", self._log_path)
            # Wait and retry -- log file may appear later
            while self._running and not p.exists():
                await asyncio.sleep(10)
            if not self._running:
                return

        logger.info("WAF audit log parser started: %s", self._log_path)

        entry_lines: list[str] = []

        async def _on_line(raw_line: str) -> None:
            nonlocal entry_lines
            line = raw_line.rstrip("\n")
            # Check for end-of-entry marker (section Z)
            m = _SECTION_RE.match(line)
            if m and m.group(1) == "Z":
                entry_lines.append(line)
                event = self._parse_entry(entry_lines)
                if event:
                    await callback(event)
                entry_lines = []
            else:
                entry_lines.append(line)

        self._tailer = AsyncLogTailer(self._log_path)
        await self._tailer.tail(_on_line)

    async def stop(self) -> None:
        self._running = False
        if self._tailer is not None:
            await self._tailer.stop()

    def _parse_entry(self, lines: list[str]) -> WafEvent | None:
        """Parse a complete audit log entry into a WafEvent."""
        if not lines:
            return None

        sections: dict[str, list[str]] = {}
        current_section = ""
        for line in lines:
            m = _SECTION_RE.match(line)
            if m:
                current_section = m.group(1)
                sections.setdefault(current_section, [])
            elif current_section:
                sections.setdefault(current_section, []).append(line)

        # Must have at least section A and H to be useful
        if "A" not in sections or "H" not in sections:
            return None

        # Parse section A
        client_ip = ""
        server_ip = ""
        a_text = " ".join(sections.get("A", []))
        a_match = _SECTION_A_RE.search(a_text)
        if a_match:
            client_ip = a_match.group("src_ip")
            server_ip = a_match.group("dst_ip")

        if not client_ip:
            return None

        # Parse section B (request line + headers)
        method = "GET"
        uri = "/"
        hostname = ""
        headers: dict[str, str] = {}
        b_lines = sections.get("B", [])
        if b_lines:
            req_match = _REQUEST_LINE_RE.match(b_lines[0])
            if req_match:
                method = req_match.group("method")
                uri = req_match.group("uri")
            for hline in b_lines[1:]:
                if ":" in hline:
                    key, _, val = hline.partition(":")
                    headers[key.strip().lower()] = val.strip()
            hostname = headers.get("host", "")

        # Parse section H (messages — most important)
        rule_id = 0
        rule_msg = ""
        severity = ""
        action = ""
        matched_data = ""
        for hline in sections.get("H", []):
            # Extract rule info from Message lines
            id_m = _RULE_ID_RE.search(hline)
            if id_m:
                rule_id = int(id_m.group(1))
            msg_m = _RULE_MSG_RE.search(hline)
            if msg_m:
                rule_msg = msg_m.group(1)
            sev_m = _SEVERITY_RE.search(hline)
            if sev_m:
                severity = sev_m.group(1)
            act_m = _ACTION_RE.match(hline)
            if act_m:
                action = act_m.group(1)
            if not action and _ACCESS_DENIED_RE.search(hline):
                action = "deny"
            if "Matched Data:" in hline:
                matched_data = hline.split("Matched Data:", 1)[-1].strip()[:200]

        # Resolve username from URI
        username = resolve_user(uri) if uri.startswith("/home/") else None

        return WafEvent(
            client_ip=client_ip,
            server_ip=server_ip,
            uri=uri[:500],
            method=method,
            rule_id=rule_id,
            rule_msg=rule_msg[:500],
            severity=severity,
            action=action,
            matched_data=matched_data,
            hostname=hostname[:200],
            username=username,
            request_headers=headers,
        )

"""Database malware scanner — scan MySQL tables for injected payloads."""
from __future__ import annotations

import asyncio
import logging
import re

logger = logging.getLogger(__name__)

# Patterns to detect in database content
_DB_PATTERNS: list[tuple[str, re.Pattern[str], str]] = [
    ("script_injection", re.compile(r"<script[^>]*>(?:eval|document\.write|String\.fromCharCode|unescape)", re.IGNORECASE), "Injected JavaScript"),
    ("base64_payload", re.compile(r"eval\s*\(\s*base64_decode\s*\(", re.IGNORECASE), "Base64 encoded payload"),
    ("spam_link", re.compile(r"https?://[^\s\"'<>]{20,}(?:\.ru|\.cn|\.tk|\.pw|pharm|viagra|cialis|casino)", re.IGNORECASE), "Spam/malware URL"),
    ("hidden_iframe", re.compile(r"<iframe[^>]+style\s*=\s*[\"'][^\"']*(?:display\s*:\s*none|height\s*:\s*0|width\s*:\s*0)", re.IGNORECASE), "Hidden iframe injection"),
    ("php_injection", re.compile(r"<\?php\s*(?:eval|assert|system|passthru)", re.IGNORECASE), "PHP code injection in DB"),
    ("encoded_redirect", re.compile(r"(?:window\.location|document\.location|meta\s+http-equiv\s*=\s*[\"']refresh)", re.IGNORECASE), "Redirect injection"),
]

# WordPress tables to scan
_WP_SCAN_TARGETS = [
    ("wp_options", "option_value", "option_id", "option_name NOT LIKE '_transient%'"),
    ("wp_posts", "post_content", "ID", "post_status != 'trash'"),
    ("wp_postmeta", "meta_value", "meta_id", "1=1"),
]

# Joomla tables to scan
_JOOMLA_SCAN_TARGETS = [
    ("jos_content", "introtext", "id", "1=1"),
    ("jos_content", "fulltext", "id", "1=1"),
    ("jos_modules", "content", "id", "1=1"),
]


_SAFE_IDENTIFIER_RE = re.compile(r"^[a-zA-Z0-9_]{1,64}$")


class DatabaseScanner:
    """Scan MySQL databases for injected malware payloads."""

    name = "database"

    def __init__(self, enabled: bool = False) -> None:
        self._enabled = enabled

    async def scan_database(
        self,
        db_name: str,
        db_user: str = "root",
        db_host: str = "localhost",
        cms_type: str = "wordpress",
        table_prefix: str = "wp_",
    ) -> list[dict]:
        """Scan a database for malware. Returns list of findings."""
        if not self._enabled:
            return []

        # Defense-in-depth: validate all identifiers internally
        for label, val in [("db_name", db_name), ("db_user", db_user), ("db_host", db_host), ("table_prefix", table_prefix)]:
            if not _SAFE_IDENTIFIER_RE.match(val.rstrip("_")):
                logger.warning("Rejected unsafe %s: %r", label, val)
                return []

        targets = _WP_SCAN_TARGETS if cms_type == "wordpress" else _JOOMLA_SCAN_TARGETS
        findings: list[dict] = []

        for table_tpl, column, id_col, where in targets:
            table = table_tpl.replace("wp_", table_prefix) if cms_type == "wordpress" else table_tpl
            # id_col / column / where come from the hardcoded
            # _WP_SCAN_TARGETS / _JOOMLA_SCAN_TARGETS tuples at module level.
            # `table` is derived from a hardcoded template with `table_prefix`
            # substituted in, and `table_prefix` has already been validated
            # above against _SAFE_IDENTIFIER_RE. Prepared statements cannot
            # parameterize table or column names, so string construction is
            # unavoidable here; backtick-quoting adds defense-in-depth.
            query = "SELECT `%s`, `%s` FROM `%s` WHERE %s LIMIT 5000" % (id_col, column, table, where)  # noqa: S608
            rows = await self._query(db_name, db_user, db_host, query)
            if rows is None:
                continue

            for row in rows:
                if len(row) < 2:
                    continue
                row_id = str(row[0])
                content = str(row[1])
                for pattern_name, pattern, desc in _DB_PATTERNS:
                    if pattern.search(content):
                        findings.append({
                            "database": db_name,
                            "table": table,
                            "column": column,
                            "row_id": row_id,
                            "pattern": pattern_name,
                            "description": desc,
                            "preview": content[:200],
                        })
                        break  # One finding per row

        logger.info("Database scan of %s complete: %d findings", db_name, len(findings))
        return findings

    @staticmethod
    async def _query(db_name: str, db_user: str, db_host: str, sql: str) -> list[list[str]] | None:
        """Run a read-only SQL query via mysql CLI. Returns rows or None."""
        try:
            proc = await asyncio.create_subprocess_exec(
                "mysql",
                "--user=%s" % db_user,
                "--host=%s" % db_host,
                "--batch",
                "--skip-column-names",
                db_name,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await asyncio.wait_for(
                proc.communicate(input=sql.encode("utf-8")),
                timeout=30,
            )
            if proc.returncode != 0:
                logger.debug("MySQL query failed for %s: %s", db_name, stderr.decode()[:200])
                return None
            rows: list[list[str]] = []
            for line in stdout.decode("utf-8", errors="replace").splitlines():
                if line.strip():
                    rows.append(line.split("\t"))
            return rows
        except (asyncio.TimeoutError, OSError) as exc:
            logger.debug("MySQL query error for %s: %s", db_name, exc)
            return None

    @property
    def enabled(self) -> bool:
        return self._enabled

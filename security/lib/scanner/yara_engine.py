"""YARA-X engine — compile and scan with yara_x (Rust-based, NOT legacy yara-python)."""

from __future__ import annotations

import asyncio
import logging
from pathlib import Path

import yara_x

from lib.models import Finding

logger = logging.getLogger(__name__)

# Default scores per YARA rule namespace
_NAMESPACE_SCORES: dict[str, int] = {
    "webshells": 40,
    "backdoors": 35,
    "miners": 30,
    "uploaders": 20,
}


class YaraEngine:
    name = "yara"

    def __init__(self, rules_dir: str, enabled: bool = True) -> None:
        self._enabled = enabled
        self._rules_dir = Path(rules_dir)
        self._rules: yara_x.Rules | None = None
        if enabled:
            self._compile_rules()

    def _compile_rules(self) -> None:
        """Compile all .yar files from the rules directory."""
        if not self._rules_dir.is_dir():
            logger.warning("YARA rules directory not found: %s", self._rules_dir)
            self._enabled = False
            return

        compiler = yara_x.Compiler()
        rule_count = 0
        for rule_file in sorted(self._rules_dir.glob("*.yar")):
            try:
                source = rule_file.read_text(encoding="utf-8")
                compiler.new_namespace(rule_file.stem)
                compiler.add_source(source)
                rule_count += 1
            except Exception:
                logger.exception("Failed to compile YARA rule: %s", rule_file)

        if rule_count > 0:
            self._rules = compiler.build()
            logger.info("YARA-X: compiled %d rule files", rule_count)
        else:
            logger.warning("No YARA rules compiled — scanner disabled")
            self._enabled = False

    async def scan(self, path: str, content: bytes) -> list[Finding]:
        """Scan content with YARA-X rules. Runs in executor (CPU-bound)."""
        if not self._enabled or self._rules is None:
            return []

        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, self._scan_sync, path, content)

    def _scan_sync(self, path: str, content: bytes) -> list[Finding]:
        """Synchronous YARA-X scan."""
        findings: list[Finding] = []
        try:
            scanner = yara_x.Scanner(self._rules)
            results = scanner.scan(content)
            for rule in results.matching_rules:
                ns = rule.namespace
                score = _NAMESPACE_SCORES.get(ns, 25)
                findings.append(Finding(
                    scanner="yara",
                    rule=rule.identifier,
                    score=score,
                    description="YARA rule match: %s/%s" % (ns, rule.identifier),
                    namespace=ns,
                    metadata={"path": path},
                ))
        except Exception:
            logger.exception("YARA-X scan error for %s", path)
        return findings

    def reload_rules(self) -> None:
        """Reload rules from disk."""
        self._enabled = True
        self._compile_rules()

    @property
    def enabled(self) -> bool:
        return self._enabled

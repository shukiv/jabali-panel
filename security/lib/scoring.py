"""Scoring engine — aggregate findings into threat scores and determine actions."""

from __future__ import annotations

import logging
from pathlib import PurePosixPath

from lib.config import JabaliConfig
from lib.models import FileEvent, Finding, ThreatScore

logger = logging.getLogger(__name__)

# Known CMS core directories — files here are likely legitimate
_CMS_CORE_DIRS = frozenset({
    "wp-admin", "wp-includes",  # WordPress
    "administrator", "libraries", "components",  # Joomla
    "core", "modules", "profiles",  # Drupal
})

# Known WordPress root files — only THESE specific files are whitelisted
_WP_ROOT_FILES = frozenset({
    "wp-activate.php", "wp-blog-header.php", "wp-comments-post.php",
    "wp-config-sample.php", "wp-config.php", "wp-cron.php",
    "wp-links-opml.php", "wp-load.php", "wp-login.php",
    "wp-mail.php", "wp-settings.php", "wp-signup.php",
    "wp-trackback.php", "xmlrpc.php",
})


class ScoringEngine:
    def __init__(self, config: JabaliConfig) -> None:
        self._score_log = config.score_log
        self._score_quarantine = config.score_quarantine
        self._score_suspend = config.score_suspend

    def evaluate(self, event: FileEvent, findings: list[Finding]) -> ThreatScore:
        """Aggregate findings, apply context multipliers, determine action."""
        if not findings:
            return ThreatScore(total=0, findings=[], action="ignore")

        # Check if file is in a known CMS core directory
        is_cms_core = self._is_cms_core_path(event.path)

        # In CMS core dirs: only YARA/ClamAV signature matches matter.
        # Heuristic, entropy, and behavior findings are noise from legitimate code.
        if is_cms_core:
            real_threats = [f for f in findings if f.scanner in ("yara", "clamav")]
            if not real_threats:
                # No signature matches — all findings are false positives from CMS code.
                # Cap action at "log" regardless of score.
                total = sum(f.score for f in findings)
                action = "log" if total >= self._score_log else "ignore"
                return ThreatScore(total=total, findings=findings, action=action)
            # Has real signature matches — score only those, but keep all findings for forensics
            total = sum(f.score for f in real_threats)
        else:
            total = sum(f.score for f in findings)

        # Context multipliers
        if event.in_uploads_dir:
            total = int(total * 1.5)

        # Determine action
        action = self._determine_action(total)

        return ThreatScore(total=total, findings=findings, action=action)

    def _determine_action(self, score: int) -> str:
        if score >= self._score_suspend:
            return "suspend"
        if score >= self._score_quarantine:
            return "quarantine"
        if score >= self._score_log:
            return "log"
        return "ignore"

    @staticmethod
    def _is_cms_core_path(path: str) -> bool:
        """Check if a file path is a known CMS core file or directory.

        Requires that the path is actually under a CMS installation by
        verifying a CMS root indicator directory exists in the path, not
        just a generic directory name like "core" or "modules".

        NOTE: wp-content/uploads/ is excluded — uploads are user content
        and can contain malware.  Theme and plugin dirs are distribution
        code and should be treated like CMS core.
        """
        p = PurePosixPath(path)
        parts = p.parts

        # CMS root indicators — directories that only exist in real CMS installs
        cms_roots = frozenset({
            "wp-content", "wp-includes", "wp-admin",   # WordPress
            "administrator", "components", "plugins",   # Joomla
        })
        has_cms_root = any(part in cms_roots for part in parts)
        if not has_cms_root:
            # No CMS root indicator — don't trust generic dir names
            # (prevents attacker from creating a "core/" dir to bypass detection)
            return False

        # Check if inside a known CMS core directory
        if any(part in _CMS_CORE_DIRS for part in parts):
            return True

        # Known WordPress root files (exact match, not prefix)
        if p.name in _WP_ROOT_FILES:
            return True

        # WordPress distribution directories under wp-content (themes, plugins,
        # mu-plugins) are CMS code — but NOT uploads (user content).
        if "wp-content" in parts:
            idx = parts.index("wp-content")
            if idx + 1 < len(parts):
                subdir = parts[idx + 1]
                if subdir in ("themes", "plugins", "mu-plugins"):
                    return True
                # uploads/ is NOT CMS core — user content can be malicious

        # Joomla template/plugin directories
        if "templates" in parts and "administrator" in parts:
            return True

        return False

    @staticmethod
    def severity_from_score(score: int) -> str:
        if score >= 100:
            return "critical"
        if score >= 70:
            return "high"
        if score >= 40:
            return "medium"
        return "low"

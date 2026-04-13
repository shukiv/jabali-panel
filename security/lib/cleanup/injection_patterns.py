"""Injection patterns — detect and remove common malware injection types."""
from __future__ import annotations

import re

# Each pattern: (name, detect_regex, removal_function_or_replacement)
# The detect regex identifies the injected section.
# The removal produces a clean version by stripping the injection.

# 1. Prepended PHP injection: <?php ...malware... ?> at file start before legitimate code
_PREPEND_PHP = re.compile(
    rb"^(<\?php\s*(?:/\*.*?\*/\s*)?(?:eval|assert|preg_replace|base64_decode|gzinflate|str_rot13)"
    rb"[^?]*\?>\s*)",
    re.DOTALL | re.IGNORECASE,
)

# 2. Appended PHP injection: malware after closing ?> or at end
_APPEND_PHP = re.compile(
    rb"(\s*<\?php\s*(?:/\*.*?\*/\s*)?(?:eval|assert|base64_decode|gzinflate|str_rot13)"
    rb"[^?]*\?>?\s*)$",
    re.DOTALL | re.IGNORECASE,
)

# 3. .htaccess injection: redirect rules inserted by malware
_HTACCESS_INJECT = re.compile(
    rb"(#\s*(?:BEGIN|start)\s*(?:inject|redirect|malware).*?#\s*(?:END|end)\s*(?:inject|redirect|malware)[^\n]*\n?)",
    re.DOTALL | re.IGNORECASE,
)

# 4. .htaccess RewriteRule to external domains (common spam redirect)
_HTACCESS_REWRITE = re.compile(
    rb"(RewriteCond\s+%\{HTTP_(?:REFERER|USER_AGENT)\}[^\n]*\n"
    rb"(?:RewriteCond[^\n]*\n)*"
    rb"RewriteRule\s+\.\*\s+https?://[^\n]+\n?)",
    re.IGNORECASE,
)

# 5. auto_prepend_file injection in .htaccess
_HTACCESS_PREPEND = re.compile(
    rb"(php_value\s+auto_prepend_file\s+[^\n]+\n?)",
    re.IGNORECASE,
)

# 6. JavaScript injection: <script> tags with obfuscated content
_JS_INJECT = re.compile(
    rb"(<script[^>]*>(?:(?:eval|document\.write|String\.fromCharCode|unescape|atob)\s*\([^<]{20,})</script>)",
    re.DOTALL | re.IGNORECASE,
)

# 7. Inline PHP injection: @eval(base64_decode(...)) or similar one-liners injected into existing code
_INLINE_EVAL = re.compile(
    rb"(@?(?:eval|assert)\s*\(\s*(?:base64_decode|gzinflate|gzuncompress|str_rot13)\s*\([^)]{20,}\)\s*\)\s*;?\s*)",
    re.IGNORECASE,
)


class InjectionCleaner:
    """Detect and remove known injection patterns from file content."""

    # (pattern_name, compiled_regex, description)
    PATTERNS: list[tuple[str, re.Pattern[bytes], str]] = [
        ("prepend_php", _PREPEND_PHP, "Prepended PHP malware block"),
        ("append_php", _APPEND_PHP, "Appended PHP malware block"),
        ("htaccess_inject", _HTACCESS_INJECT, "Marked .htaccess injection block"),
        ("htaccess_rewrite", _HTACCESS_REWRITE, ".htaccess spam redirect rules"),
        ("htaccess_prepend", _HTACCESS_PREPEND, "auto_prepend_file injection"),
        ("js_inject", _JS_INJECT, "Injected obfuscated JavaScript"),
        ("inline_eval", _INLINE_EVAL, "Inline eval/base64 one-liner"),
    ]

    def detect(self, content: bytes) -> list[tuple[str, str, int, int]]:
        """Detect injections. Returns list of (name, description, start, end)."""
        found: list[tuple[str, str, int, int]] = []
        for name, pattern, desc in self.PATTERNS:
            for m in pattern.finditer(content):
                found.append((name, desc, m.start(), m.end()))
        return found

    def clean(self, content: bytes) -> tuple[bytes, list[str]]:
        """Remove all detected injections. Returns (cleaned_content, list_of_removals)."""
        changes: list[str] = []
        cleaned = content
        for name, pattern, desc in self.PATTERNS:
            matches = list(pattern.finditer(cleaned))
            if matches:
                # Remove in reverse order to preserve offsets
                for m in reversed(matches):
                    removed_preview = m.group(0)[:80].decode("utf-8", errors="replace")
                    changes.append("Removed %s at offset %d: %s..." % (desc, m.start(), removed_preview))
                cleaned = pattern.sub(b"", cleaned)
        # Clean up multiple blank lines left after removal
        cleaned = re.sub(rb"\n{3,}", b"\n\n", cleaned)
        return cleaned, changes

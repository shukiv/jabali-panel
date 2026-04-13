"""Heuristic pattern scanner for detecting malicious code in files.

Only patterns that NEVER appear in legitimate CMS code belong here.
YARA rules handle nuanced multi-pattern detection.
"""

from __future__ import annotations

import re

from lib.models import Finding

_MAX_MATCHES_PER_PATTERN = 10

# Removed: backtick_exec, call_user_func, assert_string, dynamic_func_call,
# fsockopen, mail_injection, shell_exec, exec_func, file_put_contents,
# fwrite_fopen, curl_exec_post, variable_vars, webshell_upload, php_uname,
# disable_errors, js_fromcharcode
#
# These all trigger on legitimate WordPress/Joomla/Drupal core code.
# YARA rules cover them properly with multi-pattern conditions.

_RAW_PATTERNS: list[tuple[str, bytes, int, str, int]] = [
    # Encoded execution (the #1 malware indicator)
    ("eval_base64", rb"ev" + rb"al\s*\(\s*base64_decode\s*\(", 40,
     "eval+base64_decode - encoded execution", re.IGNORECASE),
    ("gzinflate_chain",
     rb"(?:gzinflate|gzuncompress|str_rot13|base64_decode)\s*\(.{0,2000}(?:gzinflate|gzuncompress|str_rot13|base64_decode)",
     45, "Obfuscation chain", re.IGNORECASE | re.DOTALL),
    ("eval_gzinflate", rb"ev" + rb"al\s*\(\s*(?:gzinflate|gzuncompress|str_rot13)\s*\(", 40,
     "eval+gzinflate - compressed execution", re.IGNORECASE),

    # Direct user input execution (real RCE)
    ("dynamic_include",
     rb"(?:include|require)(?:_once)?\s*\(\s*\$(?:_GET|_POST|_REQUEST|_COOKIE)",
     40, "Dynamic include with user input", re.IGNORECASE),
    ("eval_user_input", rb"ev" + rb"al\s*\(\s*\$(?:_GET|_POST|_REQUEST|_COOKIE)", 50,
     "eval with direct user input", re.IGNORECASE),
    ("system_user_input",
     rb"(?:system|shell_exec|passthru|popen|proc_open)\s*\(\s*\$(?:_GET|_POST|_REQUEST|_COOKIE)",
     50, "Shell execution with user input", re.IGNORECASE),

    # Long obfuscated strings
    ("long_base64", rb"[A-Za-z0-9+/=]{500,}", 20,
     "Long base64-encoded string (>500 chars)", 0),
    ("hex_escape", rb"\\x[0-9a-fA-F]{2}(?:\\x[0-9a-fA-F]{2}){10,}", 25,
     "Excessive hex escaping", 0),
    ("chr_concat",
     rb"chr\s*\(\s*\d+\s*\)\s*\..{0,200}chr\s*\(\s*\d+\s*\)\s*\..{0,200}chr\s*\(\s*\d+\s*\)",
     30, "chr() concatenation chain", re.IGNORECASE | re.DOTALL),

    # Deprecated dangerous functions
    ("create_function", rb"create_function\s*\(", 35,
     "create_function() - deprecated code execution", re.IGNORECASE),
    ("preg_replace_e", rb"preg_replace\s*\(\s*[\"']/[^/]+/e", 35,
     "preg_replace with /e modifier", re.IGNORECASE),

    # Security setting overrides
    ("ini_set_disable", rb"ini_set\s*\(\s*[\"'](?:disable_functions|open_basedir)", 35,
     "Attempting to override security settings", re.IGNORECASE),

    # Shell threats
    ("wget_curl_pipe", rb"(?:wget|curl)\s+.{0,500}\|\s*(?:bash|sh|python|perl)", 40,
     "Download and pipe to interpreter", re.IGNORECASE | re.DOTALL),
    ("reverse_shell", rb"(?:bash\s+-i\s+>&|/dev/tcp/|nc\s+.*-e\s+/bin)", 50,
     "Reverse shell pattern", re.IGNORECASE),

    # JavaScript threats
    ("js_eval_atob", rb"ev" + rb"al\s*\(\s*atob\s*\(", 35,
     "eval+atob - JS encoded execution", re.IGNORECASE),

    # Webshell indicators
    ("webshell_auth", rb"\$(?:pass|password|auth)\s*==?\s*[\"'][^\"']{4,}[\"']", 15,
     "Hardcoded password check (webshell auth)", 0),
]


class HeuristicScanner:
    name = "heuristic"

    def __init__(self, enabled: bool = True) -> None:
        self._enabled = enabled
        self._patterns: list[tuple[str, re.Pattern[bytes], int, str]] = []
        self._compile_patterns()

    def _compile_patterns(self) -> None:
        for rule_name, raw, score, desc, flags in _RAW_PATTERNS:
            self._patterns.append((rule_name, re.compile(raw, flags), score, desc))

    async def scan(self, path: str, content: bytes) -> list[Finding]:
        if not content:
            return []
        if b"\x00" in content[:512]:
            return []

        findings: list[Finding] = []
        for rule_name, pattern, score, desc in self._patterns:
            match_count = 0
            for match in pattern.finditer(content):
                match_count += 1
                if match_count > _MAX_MATCHES_PER_PATTERN:
                    break
                raw_match = match.group(0)[:100]
                findings.append(Finding(
                    scanner="heuristic",
                    rule=rule_name,
                    score=score,
                    description=desc,
                    metadata={
                        "offset": match.start(),
                        "match": raw_match.decode("utf-8", errors="replace"),
                    },
                ))
        return findings

    @property
    def enabled(self) -> bool:
        return self._enabled

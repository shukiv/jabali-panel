"""Bot detection rules -- user-agent patterns for filtering."""
from __future__ import annotations

from lib.webshield.models import BotRule

# Categorized bot rules
DEFAULT_RULES: list[BotRule] = [
    # -- Malicious bots (block) --
    BotRule(name="sqlmap", pattern="sqlmap", action="block", category="malicious"),
    BotRule(name="nikto", pattern="Nikto", action="block", category="malicious"),
    BotRule(name="nmap", pattern="Nmap", action="block", category="malicious"),
    BotRule(name="masscan", pattern="masscan", action="block", category="malicious"),
    BotRule(name="zgrab", pattern="zgrab", action="block", category="malicious"),
    BotRule(name="gobuster", pattern="gobuster", action="block", category="malicious"),
    BotRule(name="dirbuster", pattern="DirBuster", action="block", category="malicious"),
    BotRule(name="wpscan", pattern="WPScan", action="block", category="malicious"),
    BotRule(name="nessus", pattern="Nessus", action="block", category="malicious"),
    BotRule(name="acunetix", pattern="Acunetix", action="block", category="malicious"),
    BotRule(name="openvas", pattern="OpenVAS", action="block", category="malicious"),
    BotRule(name="havij", pattern="Havij", action="block", category="malicious"),
    BotRule(name="jorgee", pattern="Jorgee", action="block", category="malicious"),
    BotRule(name="zmeu", pattern="ZmEu", action="block", category="malicious"),

    # -- Suspicious bots (challenge) --
    BotRule(name="python_requests", pattern="python-requests", action="challenge", category="suspicious"),
    BotRule(name="python_urllib", pattern="Python-urllib", action="challenge", category="suspicious"),
    BotRule(name="curl", pattern="curl/", action="challenge", category="suspicious"),
    BotRule(name="wget", pattern="Wget/", action="challenge", category="suspicious"),
    BotRule(name="go_http", pattern="Go-http-client", action="challenge", category="suspicious"),
    BotRule(name="java", pattern="Java/", action="challenge", category="suspicious"),
    BotRule(name="libwww_perl", pattern="libwww-perl", action="challenge", category="suspicious"),
    BotRule(name="php_curl", pattern="PHP/", action="challenge", category="suspicious"),

    # -- Verified good bots (allow) --
    BotRule(name="googlebot", pattern="Googlebot", action="allow", category="verified"),
    BotRule(name="bingbot", pattern="bingbot", action="allow", category="verified"),
    BotRule(name="yandexbot", pattern="YandexBot", action="allow", category="verified"),
    BotRule(name="duckduckbot", pattern="DuckDuckBot", action="allow", category="verified"),
    BotRule(name="baiduspider", pattern="Baiduspider", action="allow", category="verified"),
    BotRule(name="uptimerobot", pattern="UptimeRobot", action="allow", category="verified"),
]


def get_rules(custom_overrides: dict[str, str] | None = None) -> list[BotRule]:
    """Get bot rules with optional overrides. overrides: {name: action}."""
    rules = [r.model_copy() for r in DEFAULT_RULES]
    if custom_overrides:
        for rule in rules:
            if rule.name in custom_overrides:
                rule.action = custom_overrides[rule.name]
    return rules

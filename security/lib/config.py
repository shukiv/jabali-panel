"""KEY=VALUE config parser — gniza4linux pattern."""

from __future__ import annotations

import logging
import os
import re
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

from lib.constants import CONFIG_FILE

logger = logging.getLogger(__name__)

_KV_RE = re.compile(r'^([A-Z_][A-Z_0-9]*)=(.*)')
_QUOTED_RE = re.compile(r'^"(.*)"$|^\'(.*)\'$')

DEFAULTS: dict[str, str] = {
    "LOG_LEVEL": "info",
    "LOG_DIR": "/var/log/jabali-security",
    "DATA_DIR": "/var/lib/jabali-security",
    "QUARANTINE_DIR": "/var/security/quarantine",
    "WORKERS": "4",
    "API_BIND": "",
    "API_PORT": "9876",
    "API_KEY": "",
    "API_SOCKET": "/run/jabali-security/jabali-security.sock",
    "WATCH_DIRS": "/home/*/public_html,/home/*/domains/*/public_html,/home/*/tmp",
    "SCAN_EXTENSIONS": ".php,.phtml,.js,.py,.sh,.cgi,.pl,.asp,.aspx,.jsp",
    "MAX_FILE_SIZE": "2097152",
    "SKIP_DIRS": ".git,node_modules,vendor,__pycache__,.cache",
    "ENTROPY_THRESHOLD": "6.0",
    "YARA_RULES_DIR": "/usr/local/jabali-security/rules",
    "SCORE_LOG": "40",
    "SCORE_QUARANTINE": "70",
    "SCORE_SUSPEND": "100",
    "PROCESS_POLL_INTERVAL": "2",
    "BEHAVIOR_TTL": "300",
    "AUTO_QUARANTINE": "yes",
    "AUTO_SUSPEND": "no",
    "AUTO_BLOCK_IP": "no",
    "NOTIFY_EMAIL": "",
    "NOTIFY_WEBHOOK": "",
    "NOTIFY_MIN_SEVERITY": "high",
    "CLAMAV_ENABLED": "no",
    "CLAMAV_SOCKET": "/var/run/clamav/clamd.ctl",
    "FRESHCLAM_ON_UPDATE": "yes",
    "FIREWALL_BACKEND": "auto",
    "UFW_ENABLED": "no",
    "BRUTEFORCE_WHITELIST_IPS": "",
    "WAF_ENABLED": "yes",
    "WAF_AUDIT_LOG": "/var/log/modsec_audit.log",
    "WAF_AUDIT_LOG_TYPE": "serial",
    "WAF_RULES_DIR": "/usr/local/share/owasp-crs/rules",
    "WAF_OVERRIDES_FILE": "/etc/modsecurity/jabali-overrides.conf",
    "WAF_CRS_AUTO_UPDATE": "yes",
    "WAF_WEB_SERVER": "auto",
    "WAF_NGINX_INCLUDE": "/etc/nginx/jabali/includes/waf.conf",
    "PROCESS_KILL_ENABLED": "no",
    "PROCESS_KILL_THRESHOLD": "70",
    "PROCESS_KILL_MIN_UID": "1000",
    "PROCESS_KILL_WHITELIST": "wp-cron.php,artisan,composer",
    "CLEANUP_ENABLED": "yes",
    "CLEANUP_AUTO": "no",
    "CLEANUP_BACKUP_DIR": "/var/lib/jabali-security/backups",
    "CLEANUP_CMS_CHECKSUMS": "yes",
    "SCHEDULED_SCAN_ENABLED": "yes",
    "SCHEDULED_SCAN_INTERVAL": "24",
    "SCHEDULED_SCAN_PATHS": "/home/*/public_html",
    "THREAT_INTEL_ENABLED": "yes",
    "THREAT_INTEL_UPDATE_INTERVAL": "6",
    "THREAT_INTEL_FEEDS": "spamhaus_drop,blocklist_de_all,tor_exit_nodes,malwarebazaar_recent",
    "THREAT_INTEL_AUTO_BLOCK": "no",
    "THREAT_INTEL_AUTO_BLOCK_THRESHOLD": "3",
    "WEBSHIELD_ENABLED": "no",
    "WEBSHIELD_RATE_LIMITING": "no",
    "WEBSHIELD_RATE_LIMIT": "10",
    "WEBSHIELD_RATE_BURST": "20",
    "WEBSHIELD_CHALLENGE_ENABLED": "yes",
    "WEBSHIELD_BOT_FILTERING": "yes",
    "WEBSHIELD_NGINX_CONF_DIR": "/etc/nginx/jabali-security",
    "GEOIP_ENABLED": "no",
    "GEOIP_DB_PATH": "/var/lib/jabali-security/GeoLite2-Country.mmdb",
    "GEOIP_MAXMIND_LICENSE_KEY": "",
    "GEOIP_BLOCKED_COUNTRIES": "",
    "GEOIP_ALLOWED_COUNTRIES": "",
    "GEOIP_ACTION": "block",
    "CHALLENGE_DIFFICULTY": "18",
    "CHALLENGE_TTL": "86400",
    "NGINX_ACCESS_LOG": "/var/log/nginx/access.log",
    "RAPIDSCAN_WORKERS": "4",
    "RAPIDSCAN_MTIME_CACHE": "yes",
    "CROWDSEC_ENABLED": "yes",
    "CROWDSEC_LAPI_URL": "http://127.0.0.1:8080",
    "CROWDSEC_BOUNCER_KEY": "",
    "CROWDSEC_SYNC_INTERVAL": "10",
}


def _sanitize_value(value: str) -> str:
    """Escape backslash, double-quote, newline, and carriage return."""
    return value.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n").replace("\r", "")


def parse_conf(filepath: Path) -> dict[str, str]:
    """Parse KEY="value" lines from a config file. Skip # comments and blanks."""
    result: dict[str, str] = {}
    if not filepath.is_file():
        return result
    text = filepath.read_text(encoding="utf-8")
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        m = _KV_RE.match(line)
        if not m:
            continue
        key = m.group(1)
        raw_value = m.group(2).strip()
        qm = _QUOTED_RE.match(raw_value)
        if qm:
            value = qm.group(1) if qm.group(1) is not None else qm.group(2)
        else:
            value = raw_value
        result[key] = value
    return result


def _atomic_write(filepath: Path, content: str) -> None:
    """Write content atomically via temp file + rename. Preserves original ownership and mode."""
    filepath.parent.mkdir(parents=True, exist_ok=True)
    # Capture original ownership/mode before overwriting
    try:
        stat = os.stat(str(filepath))
        orig_uid, orig_gid, orig_mode = stat.st_uid, stat.st_gid, stat.st_mode & 0o7777
    except FileNotFoundError:
        orig_uid, orig_gid, orig_mode = 0, 0, 0o640
    fd, tmp_path = tempfile.mkstemp(dir=str(filepath.parent), suffix=".tmp")
    try:
        os.write(fd, content.encode("utf-8"))
        try:
            os.fchmod(fd, orig_mode)
            os.fchown(fd, orig_uid, orig_gid)
        except PermissionError:
            pass  # Non-root can't chown
        os.fsync(fd)
    finally:
        os.close(fd)
    os.rename(tmp_path, str(filepath))


def update_conf_key(filepath: Path, key: str, value: str) -> None:
    """Update a single key in-place, preserving other lines and comments. Atomic write."""
    safe = _sanitize_value(value)
    new_line = f'{key}="{safe}"'
    if not filepath.is_file():
        _atomic_write(filepath, new_line + "\n")
        return

    text = filepath.read_text(encoding="utf-8")
    lines = text.splitlines()
    found = False
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("#"):
            continue
        m = _KV_RE.match(stripped)
        if m and m.group(1) == key:
            lines[i] = new_line
            found = True
            break
    if not found:
        lines.append(new_line)
    _atomic_write(filepath, "\n".join(lines) + "\n")


def _bool(value: str) -> bool:
    return value.lower() in ("yes", "true", "1", "on")


def _csv_list(value: str) -> list[str]:
    return [v.strip() for v in value.split(",") if v.strip()]


@dataclass
class JabaliConfig:
    """Typed config values loaded from the config file merged with defaults."""

    log_level: str = "info"
    log_dir: str = "/var/log/jabali-security"
    data_dir: str = "/var/lib/jabali-security"
    quarantine_dir: str = "/var/security/quarantine"
    workers: int = 4
    api_bind: str = ""
    api_port: int = 9876
    api_key: str = ""
    api_socket: str = "/run/jabali-security/jabali-security.sock"
    watch_dirs: list[str] = field(default_factory=lambda: ["/home/*/public_html", "/home/*/tmp", "/var/www"])
    scan_extensions: list[str] = field(
        default_factory=lambda: [".php", ".phtml", ".js", ".py", ".sh", ".cgi", ".pl", ".asp", ".aspx", ".jsp"]
    )
    max_file_size: int = 2097152
    skip_dirs: list[str] = field(default_factory=lambda: [".git", "node_modules", "vendor", "__pycache__", ".cache"])
    entropy_threshold: float = 4.5
    yara_rules_dir: str = "/usr/local/jabali-security/rules"
    score_log: int = 40
    score_quarantine: int = 70
    score_suspend: int = 100
    process_poll_interval: int = 2
    behavior_ttl: int = 300
    auto_quarantine: bool = True
    auto_suspend: bool = False
    auto_block_ip: bool = False
    notify_email: str = ""
    notify_webhook: str = ""
    notify_min_severity: str = "high"
    clamav_enabled: str = "auto"
    clamav_socket: str = "/var/run/clamav/clamd.ctl"
    freshclam_on_update: bool = True
    firewall_backend: str = "auto"
    ufw_enabled: bool = False
    bruteforce_whitelist_ips: list[str] = field(default_factory=list)
    waf_enabled: bool = True
    waf_audit_log: str = "/var/log/modsec_audit.log"
    waf_audit_log_type: str = "serial"
    waf_rules_dir: str = "/etc/modsecurity/crs"
    waf_overrides_file: str = "/etc/modsecurity/jabali-overrides.conf"
    waf_crs_auto_update: bool = False
    waf_web_server: str = "auto"
    waf_nginx_include: str = "/etc/nginx/jabali/includes/waf.conf"
    process_kill_enabled: bool = False
    process_kill_threshold: int = 70
    process_kill_min_uid: int = 1000
    process_kill_whitelist: list[str] = field(default_factory=lambda: ["wp-cron.php", "artisan", "composer"])
    cleanup_enabled: bool = True
    cleanup_auto: bool = False
    cleanup_backup_dir: str = "/var/lib/jabali-security/backups"
    cleanup_cms_checksums: bool = True
    scheduled_scan_enabled: bool = True
    scheduled_scan_interval: int = 24
    scheduled_scan_paths: list[str] = field(default_factory=lambda: ["/home/*/public_html"])
    threat_intel_enabled: bool = True
    threat_intel_update_interval: int = 6
    threat_intel_feeds: list[str] = field(
        default_factory=lambda: ["spamhaus_drop", "spamhaus_edrop", "blocklist_de_all", "malwarebazaar_recent"]
    )
    threat_intel_auto_block: bool = False
    threat_intel_auto_block_threshold: int = 3
    webshield_enabled: bool = False
    webshield_rate_limiting: bool = False
    webshield_rate_limit: int = 10
    webshield_rate_burst: int = 20
    webshield_challenge_enabled: bool = True
    webshield_bot_filtering: bool = True
    webshield_nginx_conf_dir: str = "/etc/nginx/jabali-security"
    geoip_enabled: bool = False
    geoip_db_path: str = "/var/lib/jabali-security/GeoLite2-Country.mmdb"
    geoip_maxmind_license_key: str = ""
    geoip_blocked_countries: list[str] = field(default_factory=list)
    geoip_allowed_countries: list[str] = field(default_factory=list)
    geoip_action: str = "block"
    challenge_difficulty: int = 18
    challenge_ttl: int = 86400
    nginx_access_log: str = "/var/log/nginx/access.log"
    rapidscan_workers: int = 4
    rapidscan_mtime_cache: bool = True
    crowdsec_enabled: str = "auto"
    crowdsec_lapi_url: str = "http://127.0.0.1:8080"
    crowdsec_bouncer_key: str = ""
    crowdsec_sync_interval: int = 10


def _safe_int(value: str, default: int, min_val: int | None = None, max_val: int | None = None) -> int:
    """Parse int with fallback default and optional range clamping."""
    try:
        result = int(value)
    except (ValueError, TypeError):
        logger.warning("Invalid integer %r, using default %d", value, default)
        return default
    if min_val is not None and result < min_val:
        return min_val
    if max_val is not None and result > max_val:
        return max_val
    return result


def _safe_float(value: str, default: float, min_val: float = 0.0, max_val: float = 8.0) -> float:
    """Parse float with fallback default and range clamping."""
    try:
        result = float(value)
    except (ValueError, TypeError):
        logger.warning("Invalid float %r, using default %s", value, default)
        return default
    return max(min_val, min(result, max_val))


def load_config(filepath: Path | None = None) -> JabaliConfig:
    """Load config from file merged with defaults, returning typed JabaliConfig."""
    if filepath is None:
        filepath = CONFIG_FILE
    merged = dict(DEFAULTS)
    merged.update(parse_conf(filepath))

    # Warn if config file is world-readable while containing an API key
    import stat
    try:
        mode = os.stat(filepath).st_mode
        if mode & stat.S_IROTH and merged.get("API_KEY"):
            logger.warning(
                "Config file %s is world-readable (mode %04o) but contains API_KEY. "
                "Fix with: chmod 640 %s", filepath, stat.S_IMODE(mode), filepath,
            )
    except OSError:
        pass

    api_bind = merged["API_BIND"]
    if api_bind and api_bind not in ("127.0.0.1", "::1", "localhost", "0.0.0.0"):  # noqa: S104
        logger.warning(
            "API_BIND=%r is not loopback — the API will be accessible from the network.",
            api_bind,
        )

    return JabaliConfig(
        log_level=merged["LOG_LEVEL"],
        log_dir=merged["LOG_DIR"],
        data_dir=merged["DATA_DIR"],
        quarantine_dir=merged["QUARANTINE_DIR"],
        workers=_safe_int(merged["WORKERS"], 4, min_val=1, max_val=32),
        api_bind=api_bind,
        api_port=_safe_int(merged["API_PORT"], 9876, min_val=1024, max_val=65535),
        api_key=merged["API_KEY"],
        api_socket=merged["API_SOCKET"],
        watch_dirs=_csv_list(merged["WATCH_DIRS"]),
        scan_extensions=_csv_list(merged["SCAN_EXTENSIONS"]),
        max_file_size=_safe_int(merged["MAX_FILE_SIZE"], 2097152, min_val=1024),
        skip_dirs=_csv_list(merged["SKIP_DIRS"]),
        entropy_threshold=_safe_float(merged["ENTROPY_THRESHOLD"], 4.5, 0.0, 8.0),
        yara_rules_dir=merged["YARA_RULES_DIR"],
        score_log=_safe_int(merged["SCORE_LOG"], 40, min_val=0),
        score_quarantine=_safe_int(merged["SCORE_QUARANTINE"], 70, min_val=0),
        score_suspend=_safe_int(merged["SCORE_SUSPEND"], 100, min_val=0),
        process_poll_interval=_safe_int(merged["PROCESS_POLL_INTERVAL"], 2, min_val=1, max_val=300),
        behavior_ttl=_safe_int(merged["BEHAVIOR_TTL"], 300, min_val=10),
        auto_quarantine=_bool(merged["AUTO_QUARANTINE"]),
        auto_suspend=_bool(merged["AUTO_SUSPEND"]),
        auto_block_ip=_bool(merged["AUTO_BLOCK_IP"]),
        notify_email=merged["NOTIFY_EMAIL"],
        notify_webhook=merged["NOTIFY_WEBHOOK"],
        notify_min_severity=merged["NOTIFY_MIN_SEVERITY"],
        clamav_enabled=merged["CLAMAV_ENABLED"],
        clamav_socket=merged["CLAMAV_SOCKET"],
        freshclam_on_update=_bool(merged["FRESHCLAM_ON_UPDATE"]),
        firewall_backend=merged["FIREWALL_BACKEND"],
        ufw_enabled=_bool(merged["UFW_ENABLED"]),
        bruteforce_whitelist_ips=_csv_list(merged["BRUTEFORCE_WHITELIST_IPS"]),
        waf_enabled=_bool(merged["WAF_ENABLED"]),
        waf_audit_log=merged["WAF_AUDIT_LOG"],
        waf_audit_log_type=merged["WAF_AUDIT_LOG_TYPE"],
        waf_rules_dir=merged["WAF_RULES_DIR"],
        waf_overrides_file=merged["WAF_OVERRIDES_FILE"],
        waf_crs_auto_update=_bool(merged["WAF_CRS_AUTO_UPDATE"]),
        waf_web_server=merged["WAF_WEB_SERVER"],
        waf_nginx_include=merged["WAF_NGINX_INCLUDE"],
        process_kill_enabled=_bool(merged["PROCESS_KILL_ENABLED"]),
        process_kill_threshold=_safe_int(merged["PROCESS_KILL_THRESHOLD"], 70, min_val=1, max_val=100),
        process_kill_min_uid=_safe_int(merged["PROCESS_KILL_MIN_UID"], 1000, min_val=0),
        process_kill_whitelist=_csv_list(merged["PROCESS_KILL_WHITELIST"]),
        cleanup_enabled=_bool(merged["CLEANUP_ENABLED"]),
        cleanup_auto=_bool(merged["CLEANUP_AUTO"]),
        cleanup_backup_dir=merged["CLEANUP_BACKUP_DIR"],
        cleanup_cms_checksums=_bool(merged["CLEANUP_CMS_CHECKSUMS"]),
        scheduled_scan_enabled=_bool(merged["SCHEDULED_SCAN_ENABLED"]),
        scheduled_scan_interval=_safe_int(merged["SCHEDULED_SCAN_INTERVAL"], 24, min_val=1, max_val=8760),
        scheduled_scan_paths=_csv_list(merged["SCHEDULED_SCAN_PATHS"]),
        threat_intel_enabled=_bool(merged["THREAT_INTEL_ENABLED"]),
        threat_intel_update_interval=_safe_int(merged["THREAT_INTEL_UPDATE_INTERVAL"], 6, min_val=1, max_val=168),
        threat_intel_feeds=_csv_list(merged["THREAT_INTEL_FEEDS"]),
        threat_intel_auto_block=_bool(merged["THREAT_INTEL_AUTO_BLOCK"]),
        threat_intel_auto_block_threshold=_safe_int(merged["THREAT_INTEL_AUTO_BLOCK_THRESHOLD"], 3, min_val=1, max_val=10),
        webshield_enabled=_bool(merged["WEBSHIELD_ENABLED"]),
        webshield_rate_limiting=_bool(merged["WEBSHIELD_RATE_LIMITING"]),
        webshield_rate_limit=_safe_int(merged["WEBSHIELD_RATE_LIMIT"], 10, min_val=1, max_val=10000),
        webshield_rate_burst=_safe_int(merged["WEBSHIELD_RATE_BURST"], 20, min_val=1, max_val=100000),
        webshield_challenge_enabled=_bool(merged["WEBSHIELD_CHALLENGE_ENABLED"]),
        webshield_bot_filtering=_bool(merged["WEBSHIELD_BOT_FILTERING"]),
        webshield_nginx_conf_dir=merged["WEBSHIELD_NGINX_CONF_DIR"],
        geoip_enabled=_bool(merged["GEOIP_ENABLED"]),
        geoip_db_path=merged["GEOIP_DB_PATH"],
        geoip_maxmind_license_key=merged["GEOIP_MAXMIND_LICENSE_KEY"],
        geoip_blocked_countries=_csv_list(merged["GEOIP_BLOCKED_COUNTRIES"]),
        geoip_allowed_countries=_csv_list(merged["GEOIP_ALLOWED_COUNTRIES"]),
        geoip_action=merged["GEOIP_ACTION"],
        challenge_difficulty=_safe_int(merged["CHALLENGE_DIFFICULTY"], 18, min_val=1, max_val=64),
        challenge_ttl=_safe_int(merged["CHALLENGE_TTL"], 86400, min_val=1, max_val=604800),
        nginx_access_log=merged["NGINX_ACCESS_LOG"],
        rapidscan_workers=_safe_int(merged["RAPIDSCAN_WORKERS"], 4, min_val=1, max_val=32),
        rapidscan_mtime_cache=_bool(merged["RAPIDSCAN_MTIME_CACHE"]),
        crowdsec_enabled=merged["CROWDSEC_ENABLED"],
        crowdsec_lapi_url=merged["CROWDSEC_LAPI_URL"],
        crowdsec_bouncer_key=merged["CROWDSEC_BOUNCER_KEY"],
        crowdsec_sync_interval=_safe_int(merged["CROWDSEC_SYNC_INTERVAL"], 10, min_val=5, max_val=300),
    )

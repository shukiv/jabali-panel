"""WebShield status/install/uninstall/rules/blocklist/geoip route handlers."""

from __future__ import annotations

import logging
import os
import re

from aiohttp import web

from api.routes.helpers import _err, _ok, _validate_ip

logger = logging.getLogger(__name__)

_COUNTRY_CODE_RE = re.compile(r"^[A-Z]{2}$")

# ISO 3166-1 alpha-2 country names (subset — full list in production)
_COUNTRY_NAMES: dict[str, str] = {
    "AF": "Afghanistan", "AL": "Albania", "DZ": "Algeria", "AR": "Argentina",
    "AU": "Australia", "AT": "Austria", "BD": "Bangladesh", "BY": "Belarus",
    "BE": "Belgium", "BR": "Brazil", "BG": "Bulgaria", "CA": "Canada",
    "CL": "Chile", "CN": "China", "CO": "Colombia", "HR": "Croatia",
    "CU": "Cuba", "CZ": "Czechia", "DK": "Denmark", "EG": "Egypt",
    "FI": "Finland", "FR": "France", "DE": "Germany", "GR": "Greece",
    "HK": "Hong Kong", "HU": "Hungary", "IN": "India", "ID": "Indonesia",
    "IR": "Iran", "IQ": "Iraq", "IE": "Ireland", "IL": "Israel",
    "IT": "Italy", "JP": "Japan", "KZ": "Kazakhstan", "KE": "Kenya",
    "KP": "North Korea", "KR": "South Korea", "KW": "Kuwait",
    "LB": "Lebanon", "LY": "Libya", "MY": "Malaysia", "MX": "Mexico",
    "MA": "Morocco", "NL": "Netherlands", "NZ": "New Zealand",
    "NG": "Nigeria", "NO": "Norway", "PK": "Pakistan", "PH": "Philippines",
    "PL": "Poland", "PT": "Portugal", "QA": "Qatar", "RO": "Romania",
    "RU": "Russia", "SA": "Saudi Arabia", "RS": "Serbia", "SG": "Singapore",
    "ZA": "South Africa", "ES": "Spain", "SE": "Sweden", "CH": "Switzerland",
    "SY": "Syria", "TW": "Taiwan", "TH": "Thailand", "TR": "Turkey",
    "UA": "Ukraine", "AE": "UAE", "GB": "United Kingdom", "US": "United States",
    "VN": "Vietnam", "YE": "Yemen",
}


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/webshield/status", get_webshield_status)
    app.router.add_post("/api/v1/webshield/install", post_webshield_install)
    app.router.add_post("/api/v1/webshield/uninstall", post_webshield_uninstall)
    app.router.add_get("/api/v1/webshield/rules", get_webshield_rules)
    app.router.add_post("/api/v1/webshield/update-blocklist", post_webshield_update_blocklist)
    app.router.add_get("/api/v1/webshield/geo-status", get_geo_status)
    app.router.add_get("/api/v1/webshield/geo-rules", get_geo_rules)
    app.router.add_post("/api/v1/webshield/geo-rules", post_geo_rules)
    app.router.add_delete("/api/v1/webshield/geo-rules/{country_code}", delete_geo_rule)
    app.router.add_post("/api/v1/webshield/geo-update-db", post_geo_update_db)


async def get_webshield_status(request: web.Request) -> web.Response:
    webshield = request.app.get("webshield")
    if not webshield:
        return _err("WebShield not enabled", 404)

    config = request.app.get("config")
    access_log = config.nginx_access_log if config else "/var/log/nginx/access.log"
    status = webshield.get_status(access_log=access_log)
    return _ok(status.model_dump())


async def post_webshield_install(request: web.Request) -> web.Response:
    webshield = request.app.get("webshield")
    if not webshield:
        return _err("WebShield not enabled", 404)

    result = await webshield.install()
    if not result.get("success"):
        return _err(result.get("error", "Install failed"), 500)

    return _ok(result)


async def post_webshield_uninstall(request: web.Request) -> web.Response:
    webshield = request.app.get("webshield")
    if not webshield:
        return _err("WebShield not enabled", 404)

    result = await webshield.uninstall()
    return _ok(result)


async def get_webshield_rules(request: web.Request) -> web.Response:
    webshield = request.app.get("webshield")
    if not webshield:
        return _err("WebShield not enabled", 404)

    rules = webshield.get_rules()
    return _ok(rules)


async def post_webshield_update_blocklist(request: web.Request) -> web.Response:
    webshield = request.app.get("webshield")
    if not webshield:
        return _err("WebShield not enabled", 404)

    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    if not isinstance(body, dict):
        return _err("Request body must be a JSON object")

    ips = body.get("ips")
    if not isinstance(ips, list):
        return _err("'ips' must be a list of IP addresses")

    # Validate each IP
    for ip in ips:
        if not isinstance(ip, str) or not _validate_ip(ip):
            return _err("Invalid IP address: %s" % ip)

    success = await webshield.update_blocked_ips(ips)
    if not success:
        return _err("Failed to write blocked IPs file", 500)

    return _ok({"updated": True, "count": len(ips)})


# ── GeoIP endpoints ────────────────────────────────────────────────────────


async def get_geo_status(request: web.Request) -> web.Response:
    """GeoIP database info and status."""
    geoip = request.app.get("geoip")
    if not geoip:
        return _ok({"enabled": False, "available": False})
    return _ok({"enabled": True, **geoip.db_info()})


async def get_geo_rules(request: web.Request) -> web.Response:
    """List blocked/allowed countries."""
    config = request.app.get("config")
    if not config:
        return _ok({"rules": [], "mode": "none"})

    blocked = config.geoip_blocked_countries
    allowed = config.geoip_allowed_countries
    action = config.geoip_action

    rules = []
    if allowed:
        for cc in allowed:
            rules.append({"country_code": cc, "country_name": _COUNTRY_NAMES.get(cc, cc), "action": "allow", "enabled": True})
        mode = "whitelist"
    elif blocked:
        for cc in blocked:
            rules.append({"country_code": cc, "country_name": _COUNTRY_NAMES.get(cc, cc), "action": action, "enabled": True})
        mode = "blocklist"
    else:
        mode = "none"

    geoip = request.app.get("geoip")
    db_info = geoip.db_info() if geoip else {"available": False}

    return _ok({"rules": rules, "mode": mode, "action": action, "enabled": config.geoip_enabled, "db": db_info})


async def post_geo_rules(request: web.Request) -> web.Response:
    """Set blocked or allowed countries. Regenerates nginx config."""
    from lib.config import load_config, update_conf_key
    from lib.constants import CONFIG_FILE

    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    countries = body.get("countries", [])
    action = body.get("action", "block")
    mode = body.get("mode", "blocklist")  # "blocklist" or "whitelist"

    if not isinstance(countries, list):
        return _err("'countries' must be a list of ISO 3166-1 alpha-2 codes")

    # Validate action FIRST (before any config writes) — SECURITY
    if action not in ("block", "challenge", "log"):
        return _err("Invalid action: %s (must be block, challenge, or log)" % action)

    # Validate country codes
    clean = []
    for cc in countries:
        cc = str(cc).upper().strip()
        if not _COUNTRY_CODE_RE.match(cc):
            return _err("Invalid country code: %s" % cc)
        clean.append(cc)

    config_file = CONFIG_FILE
    if mode == "whitelist":
        update_conf_key(config_file, "GEOIP_ALLOWED_COUNTRIES", ",".join(clean))
        update_conf_key(config_file, "GEOIP_BLOCKED_COUNTRIES", "")
    else:
        update_conf_key(config_file, "GEOIP_BLOCKED_COUNTRIES", ",".join(clean))
        update_conf_key(config_file, "GEOIP_ALLOWED_COUNTRIES", "")

    update_conf_key(config_file, "GEOIP_ACTION", action)
    if clean:
        update_conf_key(config_file, "GEOIP_ENABLED", "yes")

    # Reload in-memory config so GET reflects changes immediately
    new_config = load_config(config_file)
    old_config = request.app["config"]
    for attr in vars(new_config):
        setattr(old_config, attr, getattr(new_config, attr))

    # Write standalone GeoIP nginx config and reload
    geoip = request.app.get("geoip")
    if geoip:
        cfg = request.app["config"]
        geoip.write_nginx_configs(
            list(cfg.geoip_blocked_countries),
            list(cfg.geoip_allowed_countries),
            cfg.geoip_action,
        )

    return _ok({"countries": clean, "mode": mode, "action": action})


async def delete_geo_rule(request: web.Request) -> web.Response:
    """Remove a single country from the block/allow list."""
    from lib.config import load_config, update_conf_key
    from lib.constants import CONFIG_FILE

    cc = request.match_info["country_code"].upper().strip()
    if not _COUNTRY_CODE_RE.match(cc):
        return _err("Invalid country code: %s" % cc)

    config = load_config()
    blocked = list(config.geoip_blocked_countries)
    allowed = list(config.geoip_allowed_countries)

    if cc in blocked:
        blocked.remove(cc)
        update_conf_key(CONFIG_FILE, "GEOIP_BLOCKED_COUNTRIES", ",".join(blocked))
    if cc in allowed:
        allowed.remove(cc)
        update_conf_key(CONFIG_FILE, "GEOIP_ALLOWED_COUNTRIES", ",".join(allowed))

    # Reload in-memory config
    new_config = load_config(CONFIG_FILE)
    old_config = request.app["config"]
    for attr in vars(new_config):
        setattr(old_config, attr, getattr(new_config, attr))

    # Regenerate GeoIP nginx config
    geoip = request.app.get("geoip")
    if geoip:
        cfg = request.app["config"]
        geoip.write_nginx_configs(
            list(cfg.geoip_blocked_countries),
            list(cfg.geoip_allowed_countries),
            cfg.geoip_action,
        )

    return _ok({"removed": cc})


async def post_geo_update_db(request: web.Request) -> web.Response:
    """Download/update MaxMind GeoIP database.

    Optionally accepts account_id + license_key to write /etc/GeoIP.conf
    for the geoipupdate CLI tool and update the in-memory license key.
    """
    geoip = request.app.get("geoip")
    if not geoip:
        return _err("GeoIP not configured")

    try:
        body = await request.json()
        if not isinstance(body, dict):
            body = {}
    except Exception:
        body = {}

    account_id = body.get("account_id", "").strip()
    license_key = body.get("license_key", "").strip()

    # If credentials provided, validate them — SECURITY
    if account_id or license_key:
        # Both must be provided together
        if not account_id or not license_key:
            return _err("Both account_id and license_key are required")

        # Validate format of account_id (numeric)
        if not re.match(r"^\d+$", account_id):
            return _err("account_id must be numeric")

        # Validate format of license_key (alphanumeric + underscore)
        if not re.match(r"^[a-zA-Z0-9_]+$", license_key):
            return _err("license_key must contain only alphanumeric characters and underscore")

        # Write credentials to config file
        _write_geoip_conf(account_id, license_key)

        # Only update in-memory key AFTER successful file write
        geoip._license_key = license_key

    success, msg = await geoip.download_database()
    if not success:
        return _err(msg, 500)
    return _ok({"message": msg})


_GEOIP_CONF_PATH = "/etc/GeoIP.conf"


def _write_geoip_conf(account_id: str, license_key: str) -> None:
    """Write /etc/GeoIP.conf for geoipupdate CLI tool."""
    # Validate account_id is numeric and license_key is alphanumeric+underscore
    if not re.match(r"^\d+$", account_id):
        logger.error("Invalid account_id format (must be numeric): %s", account_id[:4] + "***")
        return
    if not re.match(r"^[a-zA-Z0-9_]+$", license_key):
        logger.error("Invalid license_key format (must be alphanumeric+_): %s", license_key[:4] + "***")
        return

    content = (
        "# GeoIP.conf - generated by jabali-security\n"
        "AccountID %s\n"
        "LicenseKey %s\n"
        "EditionIDs GeoLite2-Country\n"
        "DatabaseDirectory /var/lib/jabali-security\n"
    ) % (account_id, license_key)

    try:
        with open(_GEOIP_CONF_PATH, "w") as f:
            f.write(content)
        os.chmod(_GEOIP_CONF_PATH, 0o600)
        logger.info("GeoIP.conf updated successfully")
    except OSError as e:
        logger.error("Failed to write %s: %s", _GEOIP_CONF_PATH, e)

"""Config get/patch route handlers."""

from __future__ import annotations

import re

from aiohttp import web

from api.routes.helpers import _err, _ok
from lib.config import DEFAULTS, load_config, update_conf_key
from lib.constants import CONFIG_FILE

# Keys that cannot be changed via the REST API
_READONLY_KEYS = {"API_KEY", "API_BIND", "API_PORT"}

# Keys whose values must be valid filesystem paths under safe directories
_PATH_KEYS = {
    "LOG_DIR", "DATA_DIR", "QUARANTINE_DIR", "YARA_RULES_DIR",
    "WAF_RULES_DIR", "WAF_OVERRIDES_FILE", "WAF_NGINX_INCLUDE", "WAF_AUDIT_LOG",
    "CLAMAV_SOCKET",
}
_SAFE_PATH_PREFIXES = ("/var/", "/etc/jabali-security/", "/usr/local/jabali-security/")

_SAFE_PATH_RE = re.compile(r"^[a-zA-Z0-9/_.\-]+$")


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/config", get_config)
    app.router.add_patch("/api/v1/config", patch_config)


async def get_config(request: web.Request) -> web.Response:
    config = request.app["config"]
    data = {}
    for key in DEFAULTS:
        attr = key.lower()
        value = getattr(config, attr, None)
        if value is None:
            continue
        # Redact sensitive values
        if key == "API_KEY":
            data[key] = "set" if config.api_key else "unset"
            continue
        if key == "GEOIP_MAXMIND_LICENSE_KEY":
            data[key] = "set" if config.geoip_maxmind_license_key else ""
            continue
        elif isinstance(value, list):
            data[key] = ",".join(str(v) for v in value)
        elif isinstance(value, bool):
            data[key] = "yes" if value else "no"
        else:
            data[key] = str(value)
    return _ok(data)


async def patch_config(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    if not isinstance(body, dict):
        return _err("Request body must be a JSON object")

    # Validate all keys before applying any changes
    invalid_keys = [k for k in body if k not in DEFAULTS]
    if invalid_keys:
        return _err("Unknown config keys: %s" % ", ".join(invalid_keys))

    readonly_hit = [k for k in body if k in _READONLY_KEYS]
    if readonly_hit:
        return _err("Cannot modify read-only keys via API: %s" % ", ".join(readonly_hit))

    # Validate path-type values
    for key, value in body.items():
        if key in _PATH_KEYS:
            sv = str(value)
            if ".." in sv or not _SAFE_PATH_RE.match(sv):
                return _err("Invalid path for %s" % key)
            if not any(sv.startswith(p) for p in _SAFE_PATH_PREFIXES):
                return _err("Path for %s must be under a system directory" % key)

    updated = {}
    for key, value in body.items():
        str_value = str(value)
        update_conf_key(CONFIG_FILE, key, str_value)
        updated[key] = str_value

    # Reload in-memory config from the updated file (update in-place so
    # existing references like NotificationEngine see the new values)
    new_config = load_config(CONFIG_FILE)
    old_config = request.app["config"]
    for attr in vars(new_config):
        setattr(old_config, attr, getattr(new_config, attr))

    # Redact API_KEY in response
    if "API_KEY" in updated:
        updated["API_KEY"] = "set" if updated["API_KEY"] else "unset"

    return _ok({"updated": updated})

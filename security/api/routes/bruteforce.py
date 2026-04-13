"""IP whitelist management route handlers."""

from __future__ import annotations

from aiohttp import web

from api.routes.helpers import _err, _ok, _validate_ip
from lib.config import load_config, update_conf_key
from lib.constants import CONFIG_FILE


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/bruteforce/whitelist", get_whitelist)
    app.router.add_post("/api/v1/bruteforce/whitelist", post_whitelist)
    app.router.add_delete("/api/v1/bruteforce/whitelist/{ip}", delete_whitelist)


def _get_whitelist_ips() -> list[str]:
    """Read whitelist from config file."""
    config = load_config(CONFIG_FILE)
    return sorted(ip for ip in config.bruteforce_whitelist_ips if ip)


def _save_whitelist_ips(ips: list[str]) -> None:
    """Write whitelist to config file."""
    update_conf_key(CONFIG_FILE, "BRUTEFORCE_WHITELIST_IPS", ",".join(sorted(set(ips))))


async def get_whitelist(request: web.Request) -> web.Response:
    """List all whitelisted IPs."""
    ips = _get_whitelist_ips()
    return _ok({"whitelist": ips, "count": len(ips)})


async def post_whitelist(request: web.Request) -> web.Response:
    """Add an IP to the whitelist."""
    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    ip = body.get("ip")
    if not ip or not isinstance(ip, str):
        return _err("'ip' is required")

    if not _validate_ip(ip):
        return _err("Invalid IP address format")

    ips = _get_whitelist_ips()
    if ip not in ips:
        ips.append(ip)
        _save_whitelist_ips(ips)

    # Also unblock from CrowdSec + firewall
    crowdsec = request.app.get("crowdsec")
    if crowdsec and crowdsec.connected:
        await crowdsec.unblock_ip(ip)
    firewall = request.app.get("firewall")
    if firewall:
        await firewall.unblock_ip(ip)

    return _ok({"whitelisted": True, "ip": ip})


async def delete_whitelist(request: web.Request) -> web.Response:
    """Remove an IP from the whitelist."""
    ip = request.match_info["ip"]

    if not _validate_ip(ip):
        return _err("Invalid IP address format")

    ips = _get_whitelist_ips()
    if ip not in ips:
        return _err("IP not in whitelist", 404)

    ips.remove(ip)
    _save_whitelist_ips(ips)

    return _ok({"removed": True, "ip": ip})

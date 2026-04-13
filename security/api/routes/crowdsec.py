"""CrowdSec LAPI integration endpoints."""

from __future__ import annotations

import asyncio
import logging
import shutil

from aiohttp import web

from api.routes.helpers import _err, _ok, _validate_ip

logger = logging.getLogger(__name__)


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/crowdsec/status", get_crowdsec_status)
    app.router.add_get("/api/v1/crowdsec/decisions", get_crowdsec_decisions)
    app.router.add_get("/api/v1/crowdsec/check/{ip}", get_crowdsec_check_ip)
    app.router.add_delete("/api/v1/crowdsec/decisions/{ip}", delete_crowdsec_decision)


async def get_crowdsec_status(request: web.Request) -> web.Response:
    """CrowdSec integration status: connected, decision count, last poll."""
    client = request.app.get("crowdsec")
    if not client:
        return _ok({
            "enabled": False,
            "connected": False,
            "lapi_url": "",
            "active_decisions": 0,
            "blocked_ips": 0,
            "last_poll": "",
            "error": "CrowdSec integration not enabled",
        })

    return _ok({
        "enabled": True,
        "connected": client.connected,
        "lapi_url": request.app["config"].crowdsec_lapi_url,
        "active_decisions": client.local_decisions_count,
        "community_decisions": client.active_decisions_count,
        "blocked_ips": client.local_decisions_count,
        "last_poll": client.last_poll,
        "error": client.error,
    })


async def get_crowdsec_decisions(request: web.Request) -> web.Response:
    """List all active CrowdSec decisions."""
    client = request.app.get("crowdsec")
    if not client:
        return _err("CrowdSec not enabled", 404)

    decisions = client.get_all_decisions()
    return _ok({"decisions": decisions, "count": len(decisions)})


async def get_crowdsec_check_ip(request: web.Request) -> web.Response:
    """Check a specific IP against CrowdSec decisions (cached + live query)."""
    client = request.app.get("crowdsec")
    if not client:
        return _err("CrowdSec not enabled", 404)

    ip = request.match_info["ip"]

    # Check in-memory cache first
    cached = client.check_ip(ip)
    score = client.check_ip_score(ip)

    # Also query LAPI live for the most current data
    live = await client.query_ip(ip)

    return _ok({
        "ip": ip,
        "cached_decisions": [d.model_dump() for d in cached],
        "live_decisions": live,
        "score": score,
        "is_blocked": len(cached) > 0,
    })


async def delete_crowdsec_decision(request: web.Request) -> web.Response:
    """Remove a CrowdSec decision (unban an IP) via cscli.

    Uses create_subprocess_exec with list args (no shell injection).
    IP is validated before use.
    """
    ip = request.match_info["ip"]
    if not _validate_ip(ip):
        return _err("Invalid IP address")

    cscli = shutil.which("cscli")
    if not cscli:
        return _err("cscli not found — CrowdSec not installed", 404)

    from lib.privilege import sudo_prefix
    try:
        proc = await asyncio.create_subprocess_exec(
            *sudo_prefix(), cscli, "decisions", "delete", "--ip", ip,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=10)
        if proc.returncode != 0:
            return _err("Failed to delete decision: %s" % stderr.decode().strip(), 500)
    except asyncio.TimeoutError:
        return _err("cscli timed out", 500)

    # Remove from in-memory cache immediately
    client = request.app.get("crowdsec")
    if client:
        client._decisions.pop(ip, None)

    logger.info("CrowdSec decision deleted for %s", ip)
    return _ok({"deleted": True, "ip": ip})

"""Threat intelligence feeds/update/check route handlers."""

from __future__ import annotations

import re

from aiohttp import web

from api.routes.helpers import _err, _ok, _validate_ip
from lib.crowdsec.enrichment import enrich_reputation

_VALID_SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/threat-intel/feeds", get_threat_intel_feeds)
    app.router.add_post("/api/v1/threat-intel/update", post_threat_intel_update)
    app.router.add_get("/api/v1/threat-intel/check/ip/{ip}", get_threat_intel_check_ip)
    app.router.add_get("/api/v1/threat-intel/check/hash/{hash}", get_threat_intel_check_hash)


async def get_threat_intel_feeds(request: web.Request) -> web.Response:
    feed_mgr = request.app.get("threat_intel")
    if not feed_mgr:
        return _err("Threat intelligence not enabled", 404)

    feeds = []
    for fs in feed_mgr.feed_statuses:
        feeds.append({
            "name": fs.name,
            "source_url": fs.source_url,
            "last_update": fs.last_update.isoformat() if fs.last_update else None,
            "entry_count": fs.entry_count,
            "enabled": fs.enabled,
            "feed_type": fs.feed_type,
        })
    return _ok(feeds)


async def post_threat_intel_update(request: web.Request) -> web.Response:
    feed_mgr = request.app.get("threat_intel")
    if not feed_mgr:
        return _err("Threat intelligence not enabled", 404)

    results = await feed_mgr.update_all()
    success = sum(1 for v in results.values() if v)
    return _ok({
        "updated": results,
        "success_count": success,
        "total_count": len(results),
    })


async def get_threat_intel_check_ip(request: web.Request) -> web.Response:
    feed_mgr = request.app.get("threat_intel")
    if not feed_mgr:
        return _err("Threat intelligence not enabled", 404)

    ip = request.match_info["ip"]
    if not _validate_ip(ip):
        return _err("Invalid IP address format")

    result = feed_mgr.check_ip(ip)
    # Enrich with CrowdSec context so callers get a unified view of an IP's
    # threat posture (feeds + community intelligence) from a single lookup.
    result = enrich_reputation(result, request.app.get("crowdsec"))
    return _ok(result.model_dump())


async def get_threat_intel_check_hash(request: web.Request) -> web.Response:
    feed_mgr = request.app.get("threat_intel")
    if not feed_mgr:
        return _err("Threat intelligence not enabled", 404)

    hash_val = request.match_info["hash"]
    if not _VALID_SHA256_RE.match(hash_val):
        return _err("Invalid SHA-256 hash format (expected 64 hex characters)")

    # Check local first, then optionally remote
    remote = request.query.get("remote", "").lower() in ("1", "true", "yes")
    if remote:
        result = await feed_mgr.check_hash_remote(hash_val)
    else:
        result = feed_mgr.check_hash(hash_val)

    return _ok(result.model_dump())

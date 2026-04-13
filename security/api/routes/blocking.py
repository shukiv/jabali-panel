"""IP block/unblock/blocklist route handlers."""

from __future__ import annotations

from datetime import datetime, timezone

from aiohttp import web

from api.routes.helpers import _err, _ok, _query_int, _validate_ip


def setup_routes(app: web.Application) -> None:
    app.router.add_post("/api/v1/block", post_block)
    app.router.add_delete("/api/v1/block/{ip}", delete_block)
    app.router.add_get("/api/v1/blocklist", get_blocklist)
    app.router.add_get("/api/v1/blocklist/unified", get_blocklist_unified)


async def post_block(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    ip = body.get("ip")
    if not ip or not isinstance(ip, str):
        return _err("'ip' is required")

    if not _validate_ip(ip):
        return _err("Invalid IP address format")

    reason = str(body.get("reason", "manual block"))
    duration = body.get("duration")

    now = datetime.now(timezone.utc)
    expires_at = None
    if duration is not None:
        try:
            duration = int(duration)
            if duration < 0:
                return _err("'duration' must be a positive integer (seconds)")
        except (ValueError, TypeError):
            return _err("'duration' must be an integer (seconds)")
        from datetime import timedelta
        expires_at = (now + timedelta(seconds=duration)).isoformat()

    incidents = request.app["incidents"]
    await incidents.save_blocked_ip(ip, reason, now.isoformat(), expires_at, "api")

    # Route blocking through CrowdSec when available, fallback to our firewall
    dur = int(duration) if duration else 0
    crowdsec = request.app.get("crowdsec")
    fw_ok = False
    if crowdsec and crowdsec.connected:
        fw_ok = await crowdsec.block_ip(ip, dur, reason)
    else:
        firewall = request.app.get("firewall")
        if firewall:
            fw_ok = await firewall.block_ip(ip, dur)

    return _ok({
        "blocked": True,
        "ip": ip,
        "reason": reason,
        "expires_at": expires_at,
        "firewall": fw_ok,
    })


async def delete_block(request: web.Request) -> web.Response:
    ip = request.match_info["ip"]

    if not _validate_ip(ip):
        return _err("Invalid IP address format")

    incidents = request.app["incidents"]
    deleted = await incidents.delete_blocked_ip(ip)

    if not deleted:
        return _err("IP not found in blocklist", 404)

    # Unblock from CrowdSec when available, fallback to our firewall
    crowdsec = request.app.get("crowdsec")
    if crowdsec and crowdsec.connected:
        await crowdsec.unblock_ip(ip)
    firewall = request.app.get("firewall")
    if firewall:
        await firewall.unblock_ip(ip)


    return _ok({"unblocked": True, "ip": ip})


async def get_blocklist(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    blocked = await incidents.get_blocked_ips()
    return _ok({"blocked_ips": blocked, "count": len(blocked)})


async def get_blocklist_unified(request: web.Request) -> web.Response:
    """Unified blocklist: jabali blocked IPs + CrowdSec decisions merged.

    Query params:
        page (int): page number, default 1
        per_page (int): items per page, default 100 (max 500)
        source (str): filter by source (jabali, crowdsec, all), default all
    """
    page = _query_int(request, "page", 1, min_val=1, max_val=10000)
    per_page = _query_int(request, "per_page", 100, min_val=1, max_val=500)
    source_filter = request.query.get("source", "all")

    entries: list[dict] = []

    # Jabali blocked IPs (brute-force, threat_intel, manual)
    if source_filter in ("all", "jabali"):
        incidents = request.app["incidents"]
        for b in await incidents.get_blocked_ips():
            entries.append({
                "ip": b["ip"],
                "reason": b.get("reason", ""),
                "source": b.get("blocked_by", "manual"),
                "duration": b.get("expires_at", "permanent"),
                "blocked_at": b.get("blocked_at", ""),
            })

    # CrowdSec decisions — only local detections, not community (CAPI) bulk
    if source_filter in ("all", "crowdsec"):
        seen_ips = {e["ip"] for e in entries}
        crowdsec = request.app.get("crowdsec")
        if crowdsec:
            for d in crowdsec.get_all_decisions():
                # Skip community decisions — they didn't attack this server
                if d.get("origin") == "CAPI":
                    continue
                ip = d.get("value", "").split("/")[0]
                if ip and ip not in seen_ips:
                    entries.append({
                        "ip": ip,
                        "reason": d.get("scenario", ""),
                        "source": "crowdsec",
                        "duration": d.get("duration", ""),
                        "blocked_at": "",
                    })
                    seen_ips.add(ip)

    total = len(entries)
    start = (page - 1) * per_page
    page_entries = entries[start:start + per_page]

    return _ok({
        "blocked_ips": page_entries,
        "count": total,
        "page": page,
        "per_page": per_page,
        "pages": (total + per_page - 1) // per_page if total else 0,
    })

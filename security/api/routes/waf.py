"""WAF (ModSecurity) events/rules/enable/disable/stats/crs route handlers."""

from __future__ import annotations

from aiohttp import web

from api.routes.helpers import _err, _ok, _validate_ip


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/waf/events", get_waf_events)
    app.router.add_get("/api/v1/waf/rules", get_waf_rules)
    app.router.add_post("/api/v1/waf/rules/{rule_id}/disable", post_waf_rule_disable)
    app.router.add_post("/api/v1/waf/rules/{rule_id}/enable", post_waf_rule_enable)
    app.router.add_get("/api/v1/waf/stats", get_waf_stats)
    app.router.add_post("/api/v1/waf/crs/update", post_waf_crs_update)
    app.router.add_post("/api/v1/waf/modsecurity/enable", post_waf_modsec_enable)
    app.router.add_post("/api/v1/waf/modsecurity/disable", post_waf_modsec_disable)
    app.router.add_get("/api/v1/waf/modsecurity/status", get_waf_modsec_status)


async def get_waf_events(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]

    try:
        limit = int(request.query.get("limit", "50"))
    except (ValueError, TypeError):
        return _err("'limit' must be an integer")
    if limit < 1 or limit > 1000:
        return _err("'limit' must be between 1 and 1000")

    ip_filter = request.query.get("ip")
    if ip_filter and not _validate_ip(ip_filter):
        return _err("Invalid IP address format")

    rule_id_val = None
    rule_filter = request.query.get("rule_id")
    if rule_filter:
        try:
            rule_id_val = int(rule_filter)
        except (ValueError, TypeError):
            return _err("'rule_id' must be an integer")

    since = request.query.get("since") or None

    events = await incidents.get_waf_events(
        limit=limit,
        ip=ip_filter or None,
        rule_id=rule_id_val,
        since=since,
    )
    return _ok(events)


async def get_waf_rules(request: web.Request) -> web.Response:
    waf_rules = request.app.get("waf_rules")
    if not waf_rules:
        return _err("WAF not enabled", 404)

    rule_files = await waf_rules.list_rules()
    disabled = waf_rules.list_disabled()

    return _ok({
        "rule_files": rule_files,
        "disabled_rules": disabled,
        "web_server": waf_rules.web_server,
    })


async def post_waf_rule_disable(request: web.Request) -> web.Response:
    waf_rules = request.app.get("waf_rules")
    if not waf_rules:
        return _err("WAF not enabled", 404)

    try:
        rule_id = int(request.match_info["rule_id"])
    except (ValueError, TypeError):
        return _err("rule_id must be an integer")
    if rule_id < 1 or rule_id > 9999999:
        return _err("rule_id out of valid range")

    reloaded = await waf_rules.disable_rule(rule_id)
    return _ok({
        "disabled": True,
        "rule_id": rule_id,
        "web_server_reloaded": reloaded,
    })


async def post_waf_rule_enable(request: web.Request) -> web.Response:
    waf_rules = request.app.get("waf_rules")
    if not waf_rules:
        return _err("WAF not enabled", 404)

    try:
        rule_id = int(request.match_info["rule_id"])
    except (ValueError, TypeError):
        return _err("rule_id must be an integer")
    if rule_id < 1 or rule_id > 9999999:
        return _err("rule_id out of valid range")

    reloaded = await waf_rules.enable_rule(rule_id)
    return _ok({
        "enabled": True,
        "rule_id": rule_id,
        "web_server_reloaded": reloaded,
    })


async def get_waf_stats(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    stats = await incidents.get_waf_stats()
    return _ok(stats)


async def post_waf_modsec_enable(request: web.Request) -> web.Response:
    waf_rules = request.app.get("waf_rules")
    if not waf_rules:
        return _err("WAF not enabled", 404)
    reloaded = await waf_rules.set_modsecurity_enabled(True)
    return _ok({"enabled": True, "nginx_reloaded": reloaded})


async def post_waf_modsec_disable(request: web.Request) -> web.Response:
    waf_rules = request.app.get("waf_rules")
    if not waf_rules:
        return _err("WAF not enabled", 404)
    reloaded = await waf_rules.set_modsecurity_enabled(False)
    return _ok({"disabled": True, "nginx_reloaded": reloaded})


async def get_waf_modsec_status(request: web.Request) -> web.Response:
    waf_rules = request.app.get("waf_rules")
    if not waf_rules:
        return _err("WAF not enabled", 404)
    return _ok({"modsecurity_active": waf_rules.is_modsecurity_enabled()})


async def post_waf_crs_update(request: web.Request) -> web.Response:
    config = request.app["config"]

    from lib.waf.crs_updater import CRSUpdater
    updater = CRSUpdater(rules_dir=config.waf_rules_dir)
    result = await updater.update()

    if not result.get("success"):
        return _err(result.get("error", "CRS update failed"), 500)

    return _ok(result)

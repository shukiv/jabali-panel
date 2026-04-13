"""UFW firewall rule management route handlers."""

from __future__ import annotations

import logging

from aiohttp import web

from api.routes.helpers import _err, _ok
from lib.ufw.validators import (
    validate_action,
    validate_app_profile,
    validate_comment,
    validate_direction,
    validate_ip,
    validate_port,
    validate_protocol,
    validate_rule_number,
)

logger = logging.getLogger(__name__)


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/firewall/ufw/status", get_ufw_status)
    app.router.add_get("/api/v1/firewall/ufw/rules", get_ufw_rules)
    app.router.add_post("/api/v1/firewall/ufw/rules", post_ufw_rule)
    app.router.add_delete("/api/v1/firewall/ufw/rules/{number}", delete_ufw_rule)
    app.router.add_post("/api/v1/firewall/ufw/enable", post_ufw_enable)
    app.router.add_post("/api/v1/firewall/ufw/disable", post_ufw_disable)
    app.router.add_post("/api/v1/firewall/ufw/reload", post_ufw_reload)
    app.router.add_get("/api/v1/firewall/ufw/apps", get_ufw_apps)
    app.router.add_get("/api/v1/firewall/ufw/apps/{name}", get_ufw_app_info)
    app.router.add_post("/api/v1/firewall/ufw/apps/{name}/allow", post_ufw_app_allow)
    app.router.add_post("/api/v1/firewall/ufw/apps/{name}/deny", post_ufw_app_deny)


async def get_ufw_status(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    status = await ufw.get_status()
    return _ok(status.model_dump())


async def get_ufw_rules(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    rules = await ufw.list_rules()
    return _ok([r.model_dump() for r in rules])


async def post_ufw_rule(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    if not isinstance(body, dict):
        return _err("Request body must be a JSON object")

    # action is required
    action = validate_action(body.get("action", ""))
    if not action:
        return _err("'action' must be one of: allow, deny, reject, limit")

    # port is optional
    port = None
    if body.get("port"):
        port = validate_port(str(body["port"]))
        if port is None:
            return _err("Invalid port: must be 1-65535, range like 8000:8080, or service name")

    # protocol is optional
    protocol = None
    if body.get("protocol"):
        protocol = validate_protocol(str(body["protocol"]))
        if protocol is None:
            return _err("'protocol' must be one of: tcp, udp, any")

    # from_ip is optional
    from_ip = None
    if body.get("from_ip"):
        from_ip = validate_ip(str(body["from_ip"]))
        if from_ip is None:
            return _err("Invalid 'from_ip': must be a valid IP address or CIDR")

    # to_ip is optional
    to_ip = None
    if body.get("to_ip"):
        to_ip = validate_ip(str(body["to_ip"]))
        if to_ip is None:
            return _err("Invalid 'to_ip': must be a valid IP address or CIDR")

    # direction is optional
    direction = None
    if body.get("direction"):
        direction = validate_direction(str(body["direction"]))
        if direction is None:
            return _err("'direction' must be one of: in, out")

    # comment is optional
    comment = None
    if body.get("comment"):
        comment = validate_comment(str(body["comment"]))
        if comment is None:
            return _err("Invalid comment: printable ASCII only, max 256 chars")

    # Must have at least one target specifier
    if not any([port, from_ip, to_ip]):
        return _err("At least one of 'port', 'from_ip', or 'to_ip' is required")

    logger.info("UFW add rule: action=%s port=%s from=%s to=%s dir=%s",
                action, port, from_ip, to_ip, direction)
    success, message = await ufw.add_rule(
        action=action, port=port, protocol=protocol,
        from_ip=from_ip, to_ip=to_ip, direction=direction,
        comment=comment,
    )
    if not success:
        logger.warning("UFW add rule failed: %s", message.strip())
        return _err("Failed to add UFW rule", 500)

    return _ok({"added": True})


async def delete_ufw_rule(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    try:
        number = int(request.match_info["number"])
    except (ValueError, TypeError):
        return _err("Rule number must be an integer")

    if not validate_rule_number(number):
        return _err("Rule number must be between 1 and 9999")

    logger.info("UFW delete rule: number=%d", number)
    success, message = await ufw.remove_rule(number)
    if not success:
        logger.warning("UFW delete rule %d failed: %s", number, message.strip())
        return _err("Failed to delete UFW rule", 500)

    return _ok({"deleted": True, "rule_number": number})


async def post_ufw_enable(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    logger.info("UFW enable requested")
    success, message = await ufw.enable()
    if not success:
        logger.warning("UFW enable failed: %s", message.strip())
        return _err("Failed to enable UFW", 500)

    return _ok({"enabled": True})


async def post_ufw_disable(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    logger.info("UFW disable requested")
    success, message = await ufw.disable()
    if not success:
        logger.warning("UFW disable failed: %s", message.strip())
        return _err("Failed to disable UFW", 500)

    return _ok({"disabled": True})


async def post_ufw_reload(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    success, message = await ufw.reload()
    if not success:
        logger.warning("UFW reload failed: %s", message.strip())
        return _err("Failed to reload UFW", 500)

    return _ok({"reloaded": True})


async def get_ufw_apps(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    apps = await ufw.list_app_profiles()
    return _ok(apps)


async def get_ufw_app_info(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    name = validate_app_profile(request.match_info["name"])
    if not name:
        return _err("Invalid application profile name")

    info = await ufw.get_app_info(name)
    if not info:
        return _err("Application profile not found", 404)

    return _ok(info.model_dump())


async def post_ufw_app_allow(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    name = validate_app_profile(request.match_info["name"])
    if not name:
        return _err("Invalid application profile name")

    success, message = await ufw.allow_app(name)
    if not success:
        logger.warning("UFW app allow %r failed: %s", name, message.strip())
        return _err("Failed to allow application", 500)

    return _ok({"allowed": True, "app": name})


async def post_ufw_app_deny(request: web.Request) -> web.Response:
    ufw = request.app.get("ufw")
    if not ufw:
        return _err("UFW management not enabled", 404)

    name = validate_app_profile(request.match_info["name"])
    if not name:
        return _err("Invalid application profile name")

    success, message = await ufw.deny_app(name)
    if not success:
        logger.warning("UFW app deny %r failed: %s", name, message.strip())
        return _err("Failed to deny application", 500)

    return _ok({"denied": True, "app": name})

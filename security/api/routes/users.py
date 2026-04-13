"""User list/detail route handlers."""

from __future__ import annotations

import re

from aiohttp import web

from api.routes.helpers import _err, _ok
from api.routes.incidents import _incident_dict
from api.routes.quarantine import _quarantine_dict


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/users", get_users)
    app.router.add_get("/api/v1/users/{username}", get_user)


async def get_users(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    users = await incidents.get_user_stats()

    return _ok(users)


async def get_user(request: web.Request) -> web.Response:
    incidents_store = request.app["incidents"]
    username = request.match_info["username"]

    # Validate username: alphanumeric + underscore + hyphen + dot only
    if not re.match(r"^[a-zA-Z0-9._-]+$", username):
        return _err("Invalid username format")

    user_incidents = await incidents_store.list_incidents(limit=100, username=username)
    quarantine_records = await incidents_store.list_quarantine(username=username)

    return _ok({
        "username": username,
        "incidents": [_incident_dict(inc) for inc in user_incidents],
        "quarantine": [_quarantine_dict(r) for r in quarantine_records],
        "incident_count": len(user_incidents),
        "quarantine_count": len(quarantine_records),
    })

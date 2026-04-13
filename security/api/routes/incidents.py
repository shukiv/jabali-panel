"""Incident CRUD route handlers."""

from __future__ import annotations

from aiohttp import web

from api.routes.helpers import _err, _ok

_VALID_SEVERITIES = ("low", "medium", "high", "critical")


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/incidents", get_incidents)
    app.router.add_get("/api/v1/incidents/{id}", get_incident)
    app.router.add_post("/api/v1/incidents/{id}/resolve", post_resolve_incident)


async def get_incidents(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]

    # Parse and validate query parameters
    try:
        limit = int(request.query.get("limit", "50"))
    except (ValueError, TypeError):
        return _err("'limit' must be an integer")
    if limit < 1 or limit > 1000:
        return _err("'limit' must be between 1 and 1000")

    username = request.query.get("user") or None
    severity = request.query.get("severity") or None
    since = request.query.get("since") or None

    if severity and severity not in _VALID_SEVERITIES:
        return _err("'severity' must be one of: %s" % ", ".join(_VALID_SEVERITIES))

    results = await incidents.list_incidents(
        limit=limit, username=username, severity=severity, since=since,
    )
    return _ok([_incident_dict(inc) for inc in results])


async def get_incident(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    incident_id = request.match_info["id"]

    incident = await incidents.get(incident_id)
    if not incident:
        return _err("Incident not found", 404)

    return _ok(_incident_dict(incident))


async def post_resolve_incident(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    incident_id = request.match_info["id"]

    try:
        body = await request.json()
    except Exception:
        body = {}
    notes = str(body.get("notes", ""))

    resolved = await incidents.resolve(incident_id, notes)
    if not resolved:
        return _err("Incident not found", 404)

    return _ok({"resolved": True, "id": incident_id})


def _incident_dict(incident) -> dict:
    """Convert Incident model to JSON-safe dict."""
    return {
        "id": incident.id,
        "path": incident.file_event.path,
        "username": incident.username,
        "event_type": incident.file_event.event_type,
        "total_score": incident.total_score,
        "severity": incident.severity,
        "action_taken": incident.action_taken,
        "findings": [f.model_dump() for f in incident.findings],
        "timestamp": incident.timestamp.isoformat(),
        "resolved": incident.resolved,
        "notes": incident.notes,
    }

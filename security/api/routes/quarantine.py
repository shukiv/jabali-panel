"""Quarantine list/restore/delete route handlers."""

from __future__ import annotations

from aiohttp import web

from api.routes.helpers import _err, _ok


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/quarantine", get_quarantine)
    app.router.add_post("/api/v1/quarantine/{id}/restore", post_quarantine_restore)
    app.router.add_delete("/api/v1/quarantine/{id}", delete_quarantine)


async def get_quarantine(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    username = request.query.get("user") or None

    records = await incidents.list_quarantine(username)
    return _ok([_quarantine_dict(r) for r in records])


async def post_quarantine_restore(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    quarantine = request.app["quarantine"]
    record_id = request.match_info["id"]

    # Find the record in the database (O(1) primary-key lookup)
    record = await incidents.get_quarantine_by_id(record_id)
    if not record:
        return _err("Quarantine record not found", 404)

    restored = await quarantine.restore_file(record)
    if not restored:
        return _err("Failed to restore file", 500)

    await incidents.mark_quarantine_restored(record_id)
    return _ok({"restored": True, "id": record_id, "path": record.original_path})


async def delete_quarantine(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    quarantine = request.app["quarantine"]
    record_id = request.match_info["id"]

    # Find the record in the database (O(1) primary-key lookup)
    record = await incidents.get_quarantine_by_id(record_id)
    if not record:
        return _err("Quarantine record not found", 404)

    deleted = await quarantine.delete_quarantined(record)
    if not deleted:
        return _err("Failed to delete quarantined file", 500)

    await incidents.mark_quarantine_deleted(record_id)
    return _ok({"deleted": True, "id": record_id})


def _quarantine_dict(record) -> dict:
    """Convert QuarantineRecord to JSON-safe dict."""
    return {
        "id": record.id,
        "original_path": record.original_path,
        "quarantine_path": record.quarantine_path,
        "username": record.username,
        "timestamp": record.timestamp.isoformat(),
        "reason": record.reason,
        "incident_id": record.incident_id,
        "sha256": record.sha256,
        "restored": record.restored,
        "deleted": record.deleted,
    }

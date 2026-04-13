"""Cleanup records/file route handlers."""

from __future__ import annotations

from pathlib import Path

from aiohttp import web

from api.routes.helpers import _err, _ok, _validate_path


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/cleanup/records", get_cleanup_records)
    app.router.add_post("/api/v1/cleanup/file", post_cleanup_file)


async def get_cleanup_records(request: web.Request) -> web.Response:
    incidents = request.app["incidents"]
    records = await incidents.list_cleanups(limit=50)
    return _ok(records)


async def post_cleanup_file(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    path = body.get("path")
    if not path or not isinstance(path, str):
        return _err("'path' is required")

    if not _validate_path(path):
        return _err("Path must be under /home/ or /var/www/")

    p = Path(path)
    if p.is_symlink():
        return _err("Symlinks not allowed")
    if not p.is_file():
        return _err("File not found", 404)

    cleanup = request.app.get("cleanup")
    if not cleanup:
        return _err("Cleanup engine not available", 503)

    result = await cleanup.clean_file(path)
    # Save to DB
    await request.app["incidents"].save_cleanup(result)
    return _ok(result.model_dump(mode="json"))

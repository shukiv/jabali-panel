"""On-demand scan, full scan, scheduled scan, database scan, and rapid scan route handlers."""

from __future__ import annotations

import asyncio
import re
from pathlib import Path

from aiohttp import web

from api.routes.helpers import _err, _ok, _validate_path
from lib.models import FileEvent
from lib.safe_io import safe_read_bytes
from lib.tenant import resolve_user

_VALID_DB_NAME_RE = re.compile(r"^[a-zA-Z0-9_]+$")
_VALID_DB_HOST_RE = re.compile(r"^[a-zA-Z0-9._-]+$")


def setup_routes(app: web.Application) -> None:
    app.router.add_post("/api/v1/scan", post_scan)
    app.router.add_post("/api/v1/scan/full", post_scan_full)
    app.router.add_get("/api/v1/scan/scheduled", get_scan_scheduled)
    app.router.add_post("/api/v1/scan/database", post_scan_database)
    app.router.add_post("/api/v1/scan/rapid", post_scan_rapid)


async def post_scan(request: web.Request) -> web.Response:
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

    # Directory scan — use rapid scan engine
    if p.is_dir():
        if p.is_symlink():
            return _err("Symlinks not allowed")
        from lib.rapidscan import RapidScanEngine
        config = request.app["config"]
        engine = RapidScanEngine(config, workers=config.rapidscan_workers)
        result = await engine.scan_directory(path, request.app["scanner"], request.app["scoring"])
        return _ok(result)

    if not p.exists():
        return _err("File or directory not found: %s" % path, 404)

    # Use O_NOFOLLOW to atomically reject symlinks (no TOCTOU race)
    try:
        content = await asyncio.to_thread(safe_read_bytes, path)
    except OSError:
        return _err("Symlinks not allowed or file unreadable")

    scanner = request.app["scanner"]
    findings = await scanner.scan(path, content)

    scoring = request.app["scoring"]
    event = FileEvent(
        event_type="scan",
        path=path,
        username=resolve_user(path),
        size=len(content),
    )
    score = scoring.evaluate(event, findings)

    return _ok({
        "path": path,
        "findings": [f.model_dump() for f in findings],
        "score": score.total,
        "action": score.action,
        "severity": scoring.severity_from_score(score.total),
    })


async def post_scan_full(request: web.Request) -> web.Response:
    scheduler = request.app.get("scheduler")
    if not scheduler:
        return _err("Scan scheduler not available", 503)

    task = asyncio.create_task(scheduler.run_now())
    request.app.setdefault("_background_tasks", set()).add(task)
    task.add_done_callback(lambda t: request.app.get("_background_tasks", set()).discard(t))
    return _ok({"started": True, "status": scheduler.status})


async def get_scan_scheduled(request: web.Request) -> web.Response:
    scheduler = request.app.get("scheduler")
    if not scheduler:
        return _ok({"enabled": False})
    return _ok(scheduler.status)


async def post_scan_database(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    db_name = body.get("database")
    if not db_name or not isinstance(db_name, str):
        return _err("'database' is required")
    if not _VALID_DB_NAME_RE.match(db_name):
        return _err("Invalid database name")

    db_user = body.get("user", "root")
    if not isinstance(db_user, str) or not _VALID_DB_NAME_RE.match(db_user):
        return _err("Invalid database user")

    db_host = body.get("host", "localhost")
    if not isinstance(db_host, str) or not _VALID_DB_HOST_RE.match(db_host):
        return _err("Invalid database host")

    cms_type = body.get("cms_type", "wordpress")
    if cms_type not in ("wordpress", "joomla"):
        return _err("'cms_type' must be 'wordpress' or 'joomla'")

    table_prefix = body.get("table_prefix", "wp_")
    if not isinstance(table_prefix, str) or not _VALID_DB_NAME_RE.match(table_prefix.rstrip("_")):
        return _err("Invalid table prefix")

    from lib.scanner.database import DatabaseScanner
    scanner = DatabaseScanner(enabled=True)
    findings = await scanner.scan_database(db_name, db_user, db_host, cms_type, table_prefix)
    return _ok({"database": db_name, "findings_count": len(findings), "findings": findings})


async def post_scan_rapid(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except Exception:
        return _err("Invalid JSON body")

    path = body.get("path")
    if not path or not isinstance(path, str):
        return _err("'path' is required")
    if not _validate_path(path):
        return _err("Path must be under /home/ or /var/www/")
    if Path(path).is_symlink():
        return _err("Symlinks not allowed")
    if not Path(path).is_dir():
        return _err("Directory not found", 404)

    from lib.rapidscan import RapidScanEngine
    config = request.app["config"]
    engine = RapidScanEngine(config, workers=config.rapidscan_workers)
    if config.rapidscan_mtime_cache:
        cache_path = Path(config.data_dir) / "rapidscan_mtime.json"
        engine.set_cache_path(cache_path)
    result = await engine.scan_directory(path, request.app["scanner"], request.app["scoring"])
    return _ok(result)

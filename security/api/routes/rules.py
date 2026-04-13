"""Rules list/reload route handlers."""

from __future__ import annotations

from pathlib import Path

from aiohttp import web

from api.routes.helpers import _ok
from lib.system_tools import run_freshclam


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/rules", get_rules)
    app.router.add_post("/api/v1/rules/reload", post_rules_reload)


async def get_rules(request: web.Request) -> web.Response:
    config = request.app["config"]
    scanner = request.app["scanner"]

    rules_dir = Path(config.yara_rules_dir)
    yara_files = []
    if rules_dir.is_dir():
        for rule_file in sorted(rules_dir.glob("*.yar")):
            yara_files.append({
                "name": rule_file.name,
                "size": rule_file.stat().st_size,
            })

    clamav_available = False
    for name in scanner.scanner_names:
        if name == "clamav":
            clamav_available = True
            break

    return _ok({
        "yara_rules": yara_files,
        "yara_rules_dir": str(rules_dir),
        "yara_enabled": True,
        "clamav_enabled": clamav_available,
        "scanners": scanner.scanner_names,
    })


async def post_rules_reload(request: web.Request) -> web.Response:
    config = request.app["config"]
    scanner = request.app["scanner"]

    # Reload YARA rules
    scanner.reload_rules()
    result = {"yara_reloaded": True}

    # Optionally run freshclam
    if config.freshclam_on_update:
        success, output = await run_freshclam()
        result["freshclam_success"] = success
        result["freshclam_output"] = output.strip() if output else ""
    else:
        result["freshclam_success"] = None
        result["freshclam_output"] = "freshclam_on_update is disabled"

    return _ok(result)

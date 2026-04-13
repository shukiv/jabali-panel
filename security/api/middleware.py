"""API middleware -- authentication and request logging."""

from __future__ import annotations

import hmac
import logging
import time

from aiohttp import web

logger = logging.getLogger(__name__)


@web.middleware
async def api_key_auth(request: web.Request, handler):
    """Require X-API-Key header matching configured key."""
    # Skip auth for health check
    if request.path == "/api/v1/health":
        return await handler(request)

    config = request.app["config"]
    if not config.api_key:
        # No key configured — allow only via Unix socket (localhost-only fallback)
        if config.api_bind not in ("127.0.0.1", "::1", "localhost", ""):
            raise web.HTTPForbidden(text="API key required when binding to non-loopback address")
        return await handler(request)

    provided = request.headers.get("X-API-Key", "")
    # Use constant-time comparison to prevent timing attacks
    if not hmac.compare_digest(provided, config.api_key):
        raise web.HTTPUnauthorized(text="Invalid or missing API key")

    return await handler(request)


@web.middleware
async def request_logger(request: web.Request, handler):
    """Log all API requests with timing."""
    start = time.monotonic()
    try:
        response = await handler(request)
        elapsed = (time.monotonic() - start) * 1000
        logger.info(
            "API %s %s %d (%.1fms)",
            request.method, request.path, response.status, elapsed,
        )
        return response
    except web.HTTPException as exc:
        elapsed = (time.monotonic() - start) * 1000
        logger.warning(
            "API %s %s %d (%.1fms)",
            request.method, request.path, exc.status, elapsed,
        )
        raise

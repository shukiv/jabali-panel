"""aiohttp application factory for the REST API."""

from __future__ import annotations

import logging

from aiohttp import web

from api.middleware import api_key_auth, request_logger
from api.routes import setup_routes

logger = logging.getLogger(__name__)


def create_app() -> web.Application:
    """Create and configure the aiohttp web application.

    Component references are injected later via ComponentRegistry.populate_app().
    """
    app = web.Application(middlewares=[request_logger, api_key_auth])
    setup_routes(app)
    return app

"""Route registration package -- imports and calls all domain sub-routers."""

from __future__ import annotations

from aiohttp import web

from api.routes import (
    attack_mode,
    blocking,
    bruteforce,
    cleanup,
    config,
    core,
    crowdsec,
    incidents,
    proactive,
    quarantine,
    rules,
    scanning,
    threat_intel,
    ufw,
    users,
    waf,
    webshield,
)

_MODULES = (
    core,
    attack_mode,
    crowdsec,
    incidents,
    scanning,
    quarantine,
    users,
    blocking,
    config,
    rules,
    bruteforce,
    waf,
    proactive,
    cleanup,
    threat_intel,
    ufw,
    webshield,
)


def setup_routes(app: web.Application) -> None:
    """Register all API routes by delegating to domain modules."""
    for module in _MODULES:
        module.setup_routes(app)

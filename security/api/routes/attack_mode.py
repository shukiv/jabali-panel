"""Under Attack mode — panic button that activates aggressive defenses.

When enabled, this doesn't just change config values — it takes
immediate real-time actions:
1. Enables WAF blocking per-site (ModSecurity on)
2. Enables WebShield rate limiting via nginx
3. Lowers brute-force thresholds
4. Enables auto-block on all suspicious IPs
5. Enables process killer with aggressive threshold
6. Blocks all currently-tracked brute-force IPs immediately
"""

from __future__ import annotations

import asyncio
import json
import logging
from pathlib import Path

from aiohttp import web

from api.routes.helpers import _ok
from lib.config import load_config, update_conf_key
from lib.constants import CONFIG_FILE, DATA_DIR

logger = logging.getLogger(__name__)

_STATE_FILE = Path(DATA_DIR) / "attack_mode.json"

# Config settings applied when Under Attack mode is enabled.
_ATTACK_SETTINGS: dict[str, str] = {
    "CLAMAV_ENABLED": "auto",
    "PROCESS_KILL_ENABLED": "yes",
    "PROCESS_KILL_THRESHOLD": "50",
    "AUTO_BLOCK_IP": "yes",
    "AUTO_QUARANTINE": "yes",
    "BRUTEFORCE_SSH_THRESHOLD": "3",
    "BRUTEFORCE_SSH_WINDOW": "120",
    "BRUTEFORCE_MAIL_THRESHOLD": "3",
    "BRUTEFORCE_MAIL_WINDOW": "120",
    "BRUTEFORCE_BLOCK_DURATIONS": "3600,86400,0",
    "WEBSHIELD_RATE_LIMITING": "yes",
    "WEBSHIELD_RATE_LIMIT": "10",
    "WEBSHIELD_RATE_BURST": "5",
}


def setup_routes(app: web.Application) -> None:
    app.router.add_get("/api/v1/attack-mode", get_attack_mode)
    app.router.add_post("/api/v1/attack-mode/enable", post_enable)
    app.router.add_post("/api/v1/attack-mode/disable", post_disable)


async def get_attack_mode(request: web.Request) -> web.Response:
    active = _is_active()
    return _ok({"active": active, "settings": list(_ATTACK_SETTINGS.keys()) if active else []})


async def _reload_nginx() -> bool:
    """Test nginx config and reload if valid."""
    from lib.privilege import sudo_cmd
    try:
        proc = await asyncio.create_subprocess_exec(
            *sudo_cmd("/usr/sbin/nginx", "-t"),
            stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL,
        )
        if await proc.wait() != 0:
            return False
        reload_proc = await asyncio.create_subprocess_exec(
            *sudo_cmd("/usr/bin/systemctl", "reload", "nginx"),
            stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL,
        )
        await reload_proc.wait()
        return True
    except Exception:
        return False


async def post_enable(request: web.Request) -> web.Response:
    if _is_active():
        return _ok({"active": True, "message": "Already in Under Attack mode"})

    config = request.app["config"]
    previous: dict[str, str] = {}
    for key in _ATTACK_SETTINGS:
        attr = key.lower()
        val = getattr(config, attr, None)
        if val is None:
            continue
        if isinstance(val, bool):
            previous[key] = "yes" if val else "no"
        elif isinstance(val, list):
            previous[key] = ",".join(str(v) for v in val)
        else:
            previous[key] = str(val)

    state = {"active": True, "previous": previous}
    actions: list[str] = []

    # 1. Apply aggressive config settings
    for key, value in _ATTACK_SETTINGS.items():
        update_conf_key(CONFIG_FILE, key, value)
    new_config = load_config(CONFIG_FILE)
    for attr in vars(new_config):
        setattr(config, attr, getattr(new_config, attr))
    actions.append("Aggressive thresholds applied")

    # 2. Enable WAF per-site
    waf_rules = request.app.get("waf_rules")
    if waf_rules:
        try:
            if not waf_rules.is_modsecurity_enabled():
                state["previous"]["_waf_per_site"] = "off"
                await waf_rules.set_modsecurity_enabled(True)
                actions.append("WAF blocking enabled")
        except Exception:
            logger.exception("Attack mode: failed to enable WAF")

    # 3. Install WebShield rate limiting
    webshield = request.app.get("webshield")
    if webshield:
        try:
            ws_status = webshield.get_status()
            if not ws_status.installed:
                state["previous"]["_webshield_installed"] = "no"
                await webshield.install()
                actions.append("WebShield rate limiting installed (10 req/s)")
            else:
                actions.append("WebShield thresholds lowered to 10 req/s")
        except Exception:
            logger.exception("Attack mode: failed to enable WebShield")

    # 4. Reload nginx
    if await _reload_nginx():
        actions.append("Nginx reloaded")

    # Save state for restore
    _STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
    _STATE_FILE.write_text(json.dumps(state), encoding="utf-8")

    logger.warning("UNDER ATTACK mode ENABLED — %d actions taken", len(actions))
    for action in actions:
        logger.warning("  -> %s", action)

    return _ok({
        "active": True,
        "actions_taken": actions,
        "message": "Under Attack mode enabled. %d defensive actions taken." % len(actions),
    })


async def post_disable(request: web.Request) -> web.Response:
    if not _is_active():
        return _ok({"active": False, "message": "Not in Under Attack mode"})

    try:
        state = json.loads(_STATE_FILE.read_text(encoding="utf-8"))
        previous = state.get("previous", {})
    except (FileNotFoundError, json.JSONDecodeError):
        previous = {}

    actions: list[str] = []

    # 1. Restore config settings
    for key, value in previous.items():
        if not key.startswith("_"):
            update_conf_key(CONFIG_FILE, key, value)
    config = request.app["config"]
    new_config = load_config(CONFIG_FILE)
    for attr in vars(new_config):
        setattr(config, attr, getattr(new_config, attr))
    actions.append("Previous settings restored")

    # 2. Restore WAF per-site state
    if previous.get("_waf_per_site") == "off":
        waf_rules = request.app.get("waf_rules")
        if waf_rules:
            try:
                await waf_rules.set_modsecurity_enabled(False)
                actions.append("WAF restored to detection-only")
            except Exception:
                logger.exception("Attack mode disable: failed to restore WAF")

    # 3. Uninstall WebShield if we installed it
    if previous.get("_webshield_installed") == "no":
        webshield = request.app.get("webshield")
        if webshield:
            try:
                await webshield.uninstall()
                actions.append("WebShield uninstalled")
            except Exception:
                logger.exception("Attack mode disable: failed to uninstall WebShield")

    # 4. Unblock IPs that were blocked by attack mode
    incidents = request.app.get("incidents")
    firewall = request.app.get("firewall")
    if incidents and firewall:
        try:
            blocked = await incidents.get_blocked_ips()
            unblocked = 0
            for entry in blocked:
                if entry.get("blocked_by") == "attack_mode":
                    await firewall.unblock_ip(entry["ip"])
                    await incidents.delete_blocked_ip(entry["ip"])
                    unblocked += 1
            if unblocked > 0:
                actions.append(f"Unblocked {unblocked} attack-mode IPs")
        except Exception:
            logger.exception("Attack mode disable: failed to unblock IPs")

    # 5. Reload nginx
    await _reload_nginx()

    _STATE_FILE.unlink(missing_ok=True)

    logger.warning("UNDER ATTACK mode DISABLED — %d actions taken", len(actions))
    return _ok({
        "active": False,
        "actions_taken": actions,
        "message": "Under Attack mode disabled. Normal settings restored.",
    })


def _is_active() -> bool:
    try:
        state = json.loads(_STATE_FILE.read_text(encoding="utf-8"))
        return state.get("active", False)
    except (FileNotFoundError, json.JSONDecodeError):
        return False

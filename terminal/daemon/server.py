"""WebSocket server and API endpoints."""

import asyncio
import logging
import os
import secrets
import sys

from aiohttp import web

from .audit import AuditSession, scan_unclosed_logs
from .auth import TokenError, mint_token, verify_token
from .config import TerminalConfig, load_config
from .nonce_store import NonceStore
from .pty_bridge import run_pty_session

logger = logging.getLogger(__name__)


class TerminalServer:
    """Main server instance."""

    def __init__(self, config: TerminalConfig):
        """Initialize server."""
        self.config = config
        self.nonce_store = NonceStore(config.nonce_db_path)
        self.active_sessions = {}  # session_id -> {admin_id, ip, ws, audit}

    async def init(self) -> None:
        """Initialize server (database, scan for interrupted logs)."""
        await self.nonce_store.init()
        await scan_unclosed_logs(self.config.audit_log_dir, self.config.audit_hmac_secret)

    async def close(self) -> None:
        """Shutdown server."""
        # Close all active sessions
        for session_id, session_info in self.active_sessions.items():
            ws = session_info.get("ws")
            if ws:
                try:
                    await ws.close()
                except Exception:
                    pass

        await self.nonce_store.close()

    async def health_handler(self, request: web.Request) -> web.Response:
        """GET /health — health check."""
        return web.json_response({"status": "ok"})

    async def sessions_list_handler(self, request: web.Request) -> web.Response:
        """
        GET /api/v1/sessions — list the 100 most recent audit transcripts.

        The panel reaches this via the unix socket (root:www-data 0660) so
        caller identity is already enforced by the filesystem; the daemon
        returns metadata only (no transcript bodies) to keep the index page
        responsive.
        """
        import re

        log_dir = self.config.audit_log_dir
        try:
            entries = []
            if os.path.isdir(log_dir):
                for name in os.listdir(log_dir):
                    if not name.endswith(".log"):
                        continue
                    full = os.path.join(log_dir, name)
                    try:
                        st = os.stat(full)
                    except OSError:
                        continue
                    # Filename format: <ISO-date>_<admin>_<session-id>.log
                    # session-id is always 16 hex chars (secrets.token_hex(8)),
                    # so we split from the right to keep admin names that
                    # happen to contain underscores (e.g. 'admin_1') intact.
                    stem = name[:-4]
                    m = re.fullmatch(r"(\d{4}-\d{2}-\d{2})_(.+)_([0-9a-f]{16})", stem)
                    if m:
                        iso_date, admin, session_id = m.group(1), m.group(2), m.group(3)
                    else:
                        iso_date, admin, session_id = "", "", ""
                    sig_exists = os.path.exists(full + ".sig")
                    entries.append({
                        "name": name,
                        "date": iso_date,
                        "admin": admin,
                        "session_id": session_id,
                        "size_bytes": st.st_size,
                        "modified_at": int(st.st_mtime),
                        "sealed": sig_exists,
                    })
            entries.sort(key=lambda e: e["modified_at"], reverse=True)
            return web.json_response({"sessions": entries[:100]})
        except Exception as e:
            logger.error("sessions list failed: %s", e)
            return web.json_response({"error": "server error"}, status=500)

    async def sessions_transcript_handler(self, request: web.Request) -> web.Response:
        """
        GET /api/v1/sessions/{name}/transcript — return a single transcript.

        Accepts only sanitised filenames (no slashes, no `..`, must match the
        known pattern). The response is capped at 1 MiB; larger transcripts
        return a 413 with a message asking the admin to fetch via CLI.
        """
        name = request.match_info.get("name", "")
        # Strict whitelist: <date>_<admin>_<session>.log with limited charset.
        # Prevents path traversal and forces callers through the same filename
        # schema the daemon produces (no arbitrary reads outside audit_log_dir).
        import re

        if not re.fullmatch(r"[0-9A-Za-z._\-]{1,128}\.log", name):
            return web.json_response({"error": "invalid name"}, status=400)
        if "/" in name or ".." in name:
            return web.json_response({"error": "invalid name"}, status=400)

        log_dir = self.config.audit_log_dir
        full = os.path.join(log_dir, name)
        # realpath both sides and ensure the target is actually inside log_dir.
        try:
            resolved_dir = os.path.realpath(log_dir)
            resolved_target = os.path.realpath(full)
        except OSError:
            return web.json_response({"error": "not found"}, status=404)
        if not resolved_target.startswith(resolved_dir + os.sep) \
                and resolved_target != resolved_dir:
            return web.json_response({"error": "not found"}, status=404)
        if not os.path.isfile(resolved_target):
            return web.json_response({"error": "not found"}, status=404)

        max_bytes = 1024 * 1024
        try:
            size = os.path.getsize(resolved_target)
            if size > max_bytes:
                return web.json_response(
                    {"error": "transcript too large; use CLI"},
                    status=413,
                )
            with open(resolved_target, "rb") as fh:
                body = fh.read()
        except OSError:
            return web.json_response({"error": "not found"}, status=404)

        return web.Response(body=body, content_type="text/plain", charset="utf-8")

    async def session_handler(self, request: web.Request) -> web.Response:
        """
        POST /api/v1/session — Mint session token.

        Request: {admin_id, ip, issued_at, nonce, hmac}
        The panel signs `admin_id|ip|nonce|issued_at` with the shared HMAC
        secret. The daemon verifies the signature in constant time before
        minting; this is defense-in-depth on top of the 0660 root:www-data
        socket permissions so a write-side regression cannot forge tokens.

        Response: {ws_url, token, expires_at}

        The token is never returned in a URL; it's held in client memory
        and sent via handshake-challenge in the WebSocket auth frame (SEC-REV-2).
        """
        try:
            data = await request.json()
        except Exception:
            return web.json_response({"error": "invalid request"}, status=400)

        admin_id = data.get("admin_id")
        ip = data.get("ip")
        nonce_hex = data.get("nonce")
        issued_at = data.get("issued_at")
        hmac_sig = data.get("hmac")

        if not all([admin_id, ip, nonce_hex, issued_at, hmac_sig]):
            return web.json_response({"error": "missing fields"}, status=400)

        # Verify the request HMAC before doing any work (constant-time compare).
        import hashlib
        import hmac as hmac_mod

        try:
            secret_bytes = bytes.fromhex(self.config.hmac_secret)
            signing_string = f"{admin_id}|{ip}|{nonce_hex}|{issued_at}".encode()
            expected = hmac_mod.new(secret_bytes, signing_string, hashlib.sha256).hexdigest()
        except Exception:
            return web.json_response({"error": "invalid request"}, status=400)

        if not hmac_mod.compare_digest(str(hmac_sig), expected):
            logger.warning("Request HMAC mismatch for admin_id=%s ip=%s", admin_id, ip)
            return web.json_response({"error": "invalid request"}, status=401)

        # Reject stale requests (>30s clock skew window).
        try:
            issued_at_int = int(issued_at)
        except (TypeError, ValueError):
            return web.json_response({"error": "invalid request"}, status=400)
        now = int(__import__("time").time())
        if abs(now - issued_at_int) > 30:
            logger.warning("Session request outside clock window: issued_at=%s now=%s", issued_at_int, now)
            return web.json_response({"error": "invalid request"}, status=401)

        try:
            nonce = bytes.fromhex(nonce_hex)
            token_b64, expires_at = mint_token(
                admin_id,
                ip,
                nonce,
                self.config.hmac_secret,
                token_ttl_seconds=60,
            )

            return web.json_response(
                {
                    "ws_url": f"wss://{request.host}/terminal-ws",
                    "token": token_b64,
                    "expires_at": expires_at,
                }
            )
        except Exception as e:
            logger.error(f"Token minting failed: {e}")
            return web.json_response({"error": "token minting failed"}, status=500)

    async def ws_handler(self, request: web.Request) -> web.WebSocketResponse:
        """
        WebSocket endpoint: /terminal-ws

        Handshake-challenge flow (SEC-REV-2):
        1. Daemon sends: {type: "challenge", nonce: <32B hex>}
        2. Client responds: {type: "auth", token: <token>, nonce_response: HMAC_SHA256(token, challenge_nonce)}
        3. Daemon verifies token, accepts WS, spawns PTY, proxies traffic

        Each message is a distinct JSON payload (no code interpretation).
        """
        ws = web.WebSocketResponse()
        await ws.prepare(request)

        # Extract X-Real-IP (set by nginx)
        client_ip = request.headers.get("X-Real-IP", request.remote)

        # Generate challenge nonce (32B)
        challenge_nonce = secrets.token_bytes(32)
        challenge_nonce_hex = challenge_nonce.hex()

        # Send challenge
        await ws.send_json({"type": "challenge", "nonce": challenge_nonce_hex})

        # Wait for auth response (5s timeout per SEC-REV-1)
        try:
            auth_msg = await asyncio.wait_for(ws.receive_json(), timeout=5.0)
        except (TimeoutError, ValueError) as e:
            await ws.close(code=1008, message="401 invalid token")
            logger.warning(f"Auth timeout or invalid JSON: {e}")
            return ws

        try:
            msg_type = auth_msg.get("type")
            if msg_type != "auth":
                await ws.close(code=1008, message="401 invalid token")
                logger.warning("Expected auth message")
                return ws

            token_b64 = auth_msg.get("token")
            nonce_response = auth_msg.get("nonce_response")

            if not token_b64 or not nonce_response:
                await ws.close(code=1008, message="401 invalid token")
                return ws

            # Verify token (SEC-REV-5 strict order)
            def nonce_consumed_check(nonce: bytes) -> bool:
                """Sync check if nonce was consumed (will be done async)."""
                # This is a wrapper; actual check happens in consume()
                return False  # Placeholder

            token = verify_token(
                token_b64,
                client_ip,
                self.config.hmac_secret,
                nonce_consumed_check,
            )

            # Verify challenge response
            import hashlib
            import hmac

            expected_response = hmac.new(
                token.nonce,
                challenge_nonce,
                hashlib.sha256,
            ).hexdigest()

            if not hmac.compare_digest(nonce_response, expected_response):
                await ws.close(code=1008, message="401 invalid token")
                logger.warning("Challenge response verification failed")
                return ws

            # Consume nonce (must succeed; if already consumed, reject)
            nonce_hex = token.nonce.hex()
            nonce_consumed = await self.nonce_store.consume(nonce_hex, token.expires_at)
            if not nonce_consumed:
                await ws.close(code=1008, message="401 invalid token")
                logger.warning(f"Nonce already consumed: {nonce_hex}")
                return ws

            # Nonce is consumed and fsync'd; WS is now accepted
            # Get admin name (for audit logging) — in real use, this comes from the panel
            # For now, use a placeholder; the panel passes this in the session request
            admin_name = f"admin_{token.admin_id}"
            session_id = secrets.token_hex(8)

            # Create audit session
            audit = AuditSession(
                self.config.audit_log_dir,
                token.admin_id,
                admin_name,
                client_ip,
                session_id,
                self.config.audit_hmac_secret,
            )

            # Enforce one-session-per-admin (SEC-REV: Per-admin limit = 1)
            # Close any previous sessions for this admin
            previous_sessions = [
                (sid, s) for sid, s in list(self.active_sessions.items())
                if s["admin_id"] == token.admin_id
            ]
            for prev_sid, prev_session in previous_sessions:
                try:
                    await prev_session["ws"].close(code=1000, message="new session opened")
                except Exception:
                    pass
                self.active_sessions.pop(prev_sid, None)
                logger.info(f"Closed previous session {prev_sid} for admin_id={token.admin_id}")

            # Register new session
            self.active_sessions[session_id] = {
                "admin_id": token.admin_id,
                "ip": client_ip,
                "ws": ws,
                "audit": audit,
            }

            # PTY proxy. ws_recv returns the raw message payload so the bridge
            # can distinguish binary keystrokes (bytes) from JSON control frames
            # (str). Returning None means the peer closed the socket.
            async def ws_send(data):
                try:
                    if isinstance(data, bytes):
                        await ws.send_bytes(data)
                    else:
                        await ws.send_str(data)
                except Exception:
                    pass

            async def ws_recv():
                msg = await ws.receive()
                if msg.type == web.WSMsgType.BINARY:
                    return msg.data
                if msg.type == web.WSMsgType.TEXT:
                    return msg.data
                # CLOSE / CLOSING / CLOSED / ERROR — treat as end-of-stream.
                return None

            try:
                # run_pty_session returns the PTY exit code (or 124 on idle
                # timeout, 129 on peer-close). We don't surface it to the
                # operator yet — the audit log records it via AuditSession
                # on the finally path inside the bridge — but we discard the
                # return value explicitly so a future reader doesn't think
                # it was meant to be consumed.
                _ = await run_pty_session(
                    self.config.shell,
                    ws_send,
                    ws_recv,
                    audit,
                    admin_name,
                    session_id,
                    self.config.session_idle_seconds,
                    self.config.session_hard_seconds,
                )
            finally:
                # Clean up
                self.active_sessions.pop(session_id, None)
                await ws.close()

            return ws

        except TokenError as e:
            await ws.close(code=1008, message="401 invalid token")
            logger.warning(f"Token verification failed: {e}")
            return ws
        except Exception as e:
            await ws.close(code=1008, message="401 invalid token")
            logger.error(f"Unexpected auth error: {e}")
            return ws


async def main() -> None:
    """Start daemon."""
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    )

    # Load configuration
    try:
        config = load_config()
    except Exception as e:
        logger.error(f"Failed to load config: {e}")
        sys.exit(1)

    # Initialize server
    server = TerminalServer(config)
    await server.init()

    # Create aiohttp app for unix socket HTTP API.
    # Every route here is reachable ONLY via the unix socket (0660 root:www-data)
    # and, for /terminal/ws, via the nginx proxy that maps wss://host/terminal-ws
    # to the socket's /terminal/ws path. Never bind TCP.
    app = web.Application()
    app.router.add_get("/health", server.health_handler)
    app.router.add_post("/api/v1/session", server.session_handler)
    app.router.add_get("/api/v1/sessions", server.sessions_list_handler)
    app.router.add_get("/api/v1/sessions/{name}/transcript", server.sessions_transcript_handler)
    app.router.add_get("/terminal/ws", server.ws_handler)

    # Create runner
    runner = web.AppRunner(app)
    await runner.setup()

    # Bind to unix socket
    socket_path = config.socket_path
    os.makedirs(os.path.dirname(socket_path), exist_ok=True)
    if os.path.exists(socket_path):
        os.remove(socket_path)

    site = web.UnixSite(runner, socket_path)
    await site.start()

    # Set socket permissions (root:www-data, 0660)
    try:
        os.chmod(socket_path, 0o660)
        import grp

        www_data_gid = grp.getgrnam("www-data").gr_gid
        os.chown(socket_path, 0, www_data_gid)
    except Exception as e:
        logger.warning(f"Failed to set socket permissions: {e}")

    logger.info(f"Listening on unix socket: {socket_path}")

    # Start WebSocket server (on same TCP port, but via nginx proxy)
    # Actually, the WS server is proxied by nginx to the unix socket.
    # For now, we don't run a separate WS server; the ws_handler above
    # is called by nginx proxying to the HTTP API.
    # The real WS server listening is handled by nginx at TLS termination.

    try:
        await asyncio.Event().wait()
    except KeyboardInterrupt:
        pass
    finally:
        await runner.cleanup()
        await server.close()


if __name__ == "__main__":
    asyncio.run(main())

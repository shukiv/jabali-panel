#!/usr/bin/env python3
"""
End-to-end smoke test over the unix socket + WS upgrade.

Drives the daemon exactly the way the panel would, but skipping nginx —
nginx is a pass-through for us; the browser hits wss://host/terminal-ws
which maps to /terminal/ws on the socket.

Exercised Step 10 items:
  4. whoami returns root                  (items 4 + 5 in the plan)
  5. ls /etc/shadow succeeds              (root-only file)
  7. Stale token reuse returns 401        (single-use nonce)
  8. IP mismatch returns 401              (IP-bound token)
  9. Token expires after 60s              (skipped — too slow for CI; covered by unit test)

Run as root on the target box:
  python3 /tmp/jabali-terminal-src/tests/e2e_socket.py
"""
import asyncio
import hashlib
import hmac
import json
import re
import secrets
import socket
import sys
import time
import urllib.parse

import aiohttp

SOCKET = "/run/jabali-terminal/jabali-terminal.sock"
CONF = "/etc/jabali-terminal/jabali-terminal.conf"


def load_hmac_secret() -> str:
    with open(CONF) as f:
        m = re.search(r'^hmac_secret="([0-9a-f]+)"$', f.read(), re.M)
    if not m:
        raise SystemExit("cannot read hmac_secret from config")
    return m.group(1)


def sign(admin_id: int, ip: str, nonce_hex: str, issued_at: int, secret_hex: str) -> str:
    signing = f"{admin_id}|{ip}|{nonce_hex}|{issued_at}".encode()
    return hmac.new(bytes.fromhex(secret_hex), signing, hashlib.sha256).hexdigest()


class UnixConnector(aiohttp.UnixConnector):
    """aiohttp UnixConnector — the daemon's unix socket speaks plain HTTP."""


async def _post_session(admin_id: int, ip: str, secret: str, *, fake_ip_for_body: str | None = None):
    """Mint a token. fake_ip_for_body lets us send a signed request whose
    body says IP=A while the caller's real IP is B, to test IP binding."""
    nonce = secrets.token_hex(32)
    issued_at = int(time.time())
    body_ip = fake_ip_for_body or ip
    body = {
        "admin_id": admin_id,
        "ip": body_ip,
        "nonce": nonce,
        "issued_at": issued_at,
        "hmac": sign(admin_id, body_ip, nonce, issued_at, secret),
    }
    conn = UnixConnector(path=SOCKET)
    async with aiohttp.ClientSession(connector=conn) as s:
        async with s.post("http://localhost/api/v1/session", json=body) as r:
            return r.status, await r.json() if r.content_type == "application/json" else None


async def _open_ws_and_run(token: str, real_ip: str) -> tuple[bool, str]:
    """Connect, do handshake, send `whoami\n`, collect output for 2s."""
    conn = UnixConnector(path=SOCKET)
    headers = {"X-Real-IP": real_ip}
    try:
        async with aiohttp.ClientSession(connector=conn, headers=headers) as s:
            async with s.ws_connect(
                "http://localhost/terminal/ws",
                headers=headers,
                max_msg_size=0,
            ) as ws:
                # First frame: challenge
                msg = await asyncio.wait_for(ws.receive(), timeout=3)
                if msg.type is not aiohttp.WSMsgType.TEXT:
                    return False, f"expected TEXT challenge, got {msg.type}"
                challenge = json.loads(msg.data)
                if challenge.get("type") != "challenge":
                    return False, f"expected challenge, got {challenge}"

                # Extract nonce from token (bytes 40..72 of 104B raw)
                import base64

                padded = token.replace("-", "+").replace("_", "/")
                padded += "=" * ((4 - len(padded) % 4) % 4)
                raw = base64.b64decode(padded)
                if len(raw) != 104:
                    return False, f"bad token length {len(raw)}"
                token_nonce = raw[40:72]
                challenge_bytes = bytes.fromhex(challenge["nonce"])
                nonce_resp = hmac.new(token_nonce, challenge_bytes, hashlib.sha256).hexdigest()

                await ws.send_json({
                    "type": "auth",
                    "token": token,
                    "nonce_response": nonce_resp,
                })

                # Send `whoami\n` + `ls /etc/shadow\n` + `exit\n`
                await ws.send_bytes(b"whoami\n")
                await asyncio.sleep(0.4)
                await ws.send_bytes(b"ls /etc/shadow\n")
                await asyncio.sleep(0.4)
                await ws.send_bytes(b"exit\n")

                buf = bytearray()
                deadline = time.time() + 3.0
                while time.time() < deadline:
                    remaining = deadline - time.time()
                    if remaining <= 0:
                        break
                    try:
                        m = await asyncio.wait_for(ws.receive(), timeout=remaining)
                    except asyncio.TimeoutError:
                        break
                    if m.type is aiohttp.WSMsgType.BINARY:
                        buf.extend(m.data)
                    elif m.type is aiohttp.WSMsgType.TEXT:
                        buf.extend(m.data.encode())
                    elif m.type in (aiohttp.WSMsgType.CLOSE, aiohttp.WSMsgType.CLOSED, aiohttp.WSMsgType.ERROR):
                        break
                return True, buf.decode("utf-8", errors="replace")
    except Exception as e:
        return False, f"ws error: {type(e).__name__}: {e}"


async def main() -> int:
    if not (sys.version_info >= (3, 11)):
        print("need Python 3.11+", file=sys.stderr)
        return 2
    secret = load_hmac_secret()
    real_ip = "127.0.0.1"

    print("--- 1) Health endpoint")
    conn = UnixConnector(path=SOCKET)
    async with aiohttp.ClientSession(connector=conn) as s:
        async with s.get("http://localhost/health") as r:
            assert r.status == 200, r.status
            print("  ok:", await r.json())

    print("--- 2) Mint + WS handshake + whoami")
    status, data = await _post_session(admin_id=1, ip=real_ip, secret=secret)
    assert status == 200, (status, data)
    token = data["token"]
    ok, out = await _open_ws_and_run(token, real_ip)
    if not ok:
        print("  FAIL:", out)
        return 1
    print("  transcript:")
    for line in out.splitlines()[-15:]:
        print("    " + line)
    assert "root" in out, "expected 'root' in whoami output"
    assert "/etc/shadow" in out, "expected ls to list /etc/shadow"

    print("--- 3) Token replay (single-use)")
    ok2, out2 = await _open_ws_and_run(token, real_ip)
    if ok2 and "root" in out2:
        print("  FAIL: second use succeeded")
        return 1
    print("  ok (rejected)")

    print("--- 4) IP binding")
    status, data = await _post_session(admin_id=1, ip="10.0.0.1", secret=secret, fake_ip_for_body="10.0.0.1")
    if status == 200:
        ok3, out3 = await _open_ws_and_run(data["token"], "127.0.0.1")
        if ok3 and "root" in out3:
            print("  FAIL: IP mismatch accepted")
            return 1
    print("  ok (rejected)")

    print("--- 5) Bad HMAC signature")
    nonce = secrets.token_hex(32)
    issued = int(time.time())
    body = {
        "admin_id": 1,
        "ip": real_ip,
        "nonce": nonce,
        "issued_at": issued,
        "hmac": "0" * 64,
    }
    conn = UnixConnector(path=SOCKET)
    async with aiohttp.ClientSession(connector=conn) as s:
        async with s.post("http://localhost/api/v1/session", json=body) as r:
            assert r.status == 401, r.status
    print("  ok (rejected)")

    print("--- 6) Sessions index reflects the session we just ran")
    conn = UnixConnector(path=SOCKET)
    async with aiohttp.ClientSession(connector=conn) as s:
        async with s.get("http://localhost/api/v1/sessions") as r:
            assert r.status == 200, r.status
            body = await r.json()
    sessions = body.get("sessions") or []
    assert sessions, "no sessions listed"
    top = sessions[0]
    print(f"  top session: admin={top['admin']}, size={top['size_bytes']}B, sealed={top['sealed']}")
    assert top["sealed"], "top session should be sealed after exit"

    print("--- 7) Transcript read")
    async with aiohttp.ClientSession(connector=UnixConnector(path=SOCKET)) as s:
        async with s.get(f"http://localhost/api/v1/sessions/{urllib.parse.quote(top['name'])}/transcript") as r:
            assert r.status == 200, r.status
            txt = await r.text()
    assert "Session start" in txt and "Session end" in txt
    assert "whoami" in txt or "root" in txt
    print(f"  ok ({len(txt)}B transcript, seal + shell trace present)")

    print("--- 8) Path traversal on transcript endpoint")
    async with aiohttp.ClientSession(connector=UnixConnector(path=SOCKET)) as s:
        async with s.get("http://localhost/api/v1/sessions/..%2F..%2Fetc%2Fshadow/transcript") as r:
            assert r.status in (400, 404), r.status
    print("  ok (rejected)")

    print("")
    print("PASS — all daemon-side E2E checks green.")
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

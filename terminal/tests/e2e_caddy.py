#!/usr/bin/env python3
"""
End-to-end through Caddy (TLS :8443), not the unix socket.

Proves the public URL path — wss://host:8443/terminal-ws — routes to the
daemon's /terminal/ws handler via the installed Caddy block, and a full
handshake + PTY + whoami round-trip works.

Run on the target box, as root (config read needs root):
  /usr/local/jabali-terminal/venv/bin/python tests/e2e_caddy.py
"""
import asyncio
import base64
import hashlib
import hmac
import json
import re
import secrets
import socket
import ssl
import sys
import time

import aiohttp

CONF = "/etc/jabali-terminal/jabali-terminal.conf"
BASE_URL = "https://127.0.0.1:8443"
WS_URL = "wss://127.0.0.1:8443/terminal-ws"


def load_hmac_secret() -> str:
    with open(CONF) as f:
        m = re.search(r'^hmac_secret="([0-9a-f]+)"$', f.read(), re.M)
    if not m:
        raise SystemExit("cannot read hmac_secret from config")
    return m.group(1)


def sign(admin_id: int, ip: str, nonce_hex: str, issued_at: int, secret_hex: str) -> str:
    s = f"{admin_id}|{ip}|{nonce_hex}|{issued_at}".encode()
    return hmac.new(bytes.fromhex(secret_hex), s, hashlib.sha256).hexdigest()


async def main() -> int:
    # Panel cert is self-signed per Caddyfile; skip TLS verification since
    # we're on the box that installed the cert.
    ssl_ctx = ssl.create_default_context()
    ssl_ctx.check_hostname = False
    ssl_ctx.verify_mode = ssl.CERT_NONE

    secret = load_hmac_secret()
    # The "ip" the daemon will see is whatever Caddy sets in X-Real-IP,
    # which for a loopback caller is 127.0.0.1.
    real_ip = "127.0.0.1"

    # Mint a token via the daemon's unix socket (Caddy doesn't expose the
    # mint endpoint — that's handled by the panel's TerminalSessionController
    # which then calls the daemon over the socket).
    conn_unix = aiohttp.UnixConnector(path="/run/jabali-terminal/jabali-terminal.sock")
    nonce = secrets.token_hex(32)
    issued_at = int(time.time())
    payload = {
        "admin_id": 1,
        "ip": real_ip,
        "nonce": nonce,
        "issued_at": issued_at,
        "hmac": sign(1, real_ip, nonce, issued_at, secret),
    }
    async with aiohttp.ClientSession(connector=conn_unix) as s:
        async with s.post("http://localhost/api/v1/session", json=payload) as r:
            if r.status != 200:
                print(f"mint failed: {r.status}")
                return 1
            data = await r.json()
    token = data["token"]

    # Open WS through Caddy, not the socket.
    conn = aiohttp.TCPConnector(ssl=ssl_ctx)
    async with aiohttp.ClientSession(connector=conn) as s:
        async with s.ws_connect(WS_URL, ssl=ssl_ctx, max_msg_size=0) as ws:
            msg = await asyncio.wait_for(ws.receive(), timeout=5)
            if msg.type is not aiohttp.WSMsgType.TEXT:
                print(f"expected TEXT challenge, got {msg.type}")
                return 1
            challenge = json.loads(msg.data)

            # Extract nonce from token; HMAC with the challenge nonce.
            padded = token.replace("-", "+").replace("_", "/")
            padded += "=" * ((4 - len(padded) % 4) % 4)
            raw = base64.b64decode(padded)
            token_nonce = raw[40:72]
            challenge_bytes = bytes.fromhex(challenge["nonce"])
            nonce_resp = hmac.new(token_nonce, challenge_bytes, hashlib.sha256).hexdigest()

            await ws.send_json({
                "type": "auth",
                "token": token,
                "nonce_response": nonce_resp,
            })

            await ws.send_bytes(b"whoami\n")
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
            out = buf.decode("utf-8", errors="replace")

    print("--- transcript via Caddy:")
    for line in out.splitlines()[-10:]:
        print("    " + line)

    if "root" not in out:
        print("FAIL: no 'root' in whoami output")
        return 1
    print("\nPASS — full TLS-through-Caddy path is green.")
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

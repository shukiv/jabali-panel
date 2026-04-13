"""Tests for token generation and verification."""

import secrets
import time

import pytest

from daemon.auth import TokenError, mint_token, verify_token


@pytest.fixture
def hmac_secret():
    """Generate a test HMAC secret (64 hex chars = 32 bytes)."""
    return secrets.token_hex(32)


@pytest.fixture
def nonce():
    """Generate a test nonce (32 bytes)."""
    return secrets.token_bytes(32)


def test_mint_token(hmac_secret, nonce):
    """Test token minting."""
    admin_id = 1
    ip = "127.0.0.1"

    token_b64, expires_at = mint_token(admin_id, ip, nonce, hmac_secret)

    assert isinstance(token_b64, str)
    assert isinstance(expires_at, int)
    assert expires_at > int(time.time())


def test_verify_token_valid(hmac_secret, nonce):
    """Test token verification with valid token."""
    admin_id = 1
    ip = "127.0.0.1"

    token_b64, expires_at = mint_token(admin_id, ip, nonce, hmac_secret)

    def nonce_check(n):
        return False  # Not consumed

    token = verify_token(token_b64, ip, hmac_secret, nonce_check)

    assert token.admin_id == admin_id
    assert token.ip == ip
    assert token.nonce == nonce
    assert token.expires_at == expires_at


def test_verify_token_expired(hmac_secret, nonce):
    """Test token verification with expired token."""
    admin_id = 1
    ip = "127.0.0.1"

    # Mint token with -1s TTL (already expired)
    token_b64 = _mint_token_with_ttl(admin_id, ip, nonce, hmac_secret, ttl_seconds=-1)

    def nonce_check(n):
        return False

    with pytest.raises(TokenError):
        verify_token(token_b64, ip, hmac_secret, nonce_check)


def test_verify_token_wrong_ip(hmac_secret, nonce):
    """Test token verification with wrong IP."""
    admin_id = 1
    ip = "127.0.0.1"
    wrong_ip = "192.168.1.1"

    token_b64, _ = mint_token(admin_id, ip, nonce, hmac_secret)

    def nonce_check(n):
        return False

    with pytest.raises(TokenError):
        verify_token(token_b64, wrong_ip, hmac_secret, nonce_check)


def test_verify_token_wrong_hmac_secret(hmac_secret, nonce):
    """Test token verification with wrong HMAC secret."""
    admin_id = 1
    ip = "127.0.0.1"

    token_b64, _ = mint_token(admin_id, ip, nonce, hmac_secret)

    wrong_secret = secrets.token_hex(32)

    def nonce_check(n):
        return False

    with pytest.raises(TokenError):
        verify_token(token_b64, ip, wrong_secret, nonce_check)


def test_verify_token_malformed(hmac_secret):
    """Test token verification with malformed token."""
    def nonce_check(n):
        return False

    with pytest.raises(TokenError):
        verify_token("invalid-base64!@#", "127.0.0.1", hmac_secret, nonce_check)


def test_verify_token_ipv6(hmac_secret, nonce):
    """Test token generation and verification with IPv6."""
    admin_id = 1
    ip = "::1"

    token_b64, _ = mint_token(admin_id, ip, nonce, hmac_secret)

    def nonce_check(n):
        return False

    token = verify_token(token_b64, ip, hmac_secret, nonce_check)

    assert token.admin_id == admin_id
    assert token.ip == ip


def _mint_token_with_ttl(admin_id, ip, nonce, hmac_secret, ttl_seconds):
    """Helper to mint token with custom TTL."""
    import base64
    import hashlib
    import hmac as hmac_module
    import struct

    now = int(time.time())
    expires_at = now + ttl_seconds

    # Normalize IP
    import ipaddress

    addr = ipaddress.ip_address(ip)
    if isinstance(addr, ipaddress.IPv4Address):
        ip_bytes = b"\x00" * 12 + addr.packed
    else:
        ip_bytes = addr.packed

    # Construct body
    body = struct.pack("<Q", admin_id)
    body += ip_bytes
    body += struct.pack("<Q", now)
    body += struct.pack("<Q", expires_at)
    body += nonce

    # Compute HMAC
    secret_bytes = bytes.fromhex(hmac_secret)
    h = hmac_module.new(secret_bytes, body, hashlib.sha256)
    hmac_digest = h.digest()

    token_bytes = body + hmac_digest
    return base64.urlsafe_b64encode(token_bytes).decode("ascii")

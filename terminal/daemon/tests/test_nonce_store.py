"""Tests for persistent nonce store."""

import os
import tempfile

import pytest

from daemon.nonce_store import NonceStore


@pytest.fixture
async def nonce_store():
    """Create a temporary nonce store."""
    with tempfile.TemporaryDirectory() as tmpdir:
        db_path = os.path.join(tmpdir, "nonces.db")
        store = NonceStore(db_path)
        await store.init()
        yield store
        await store.close()


@pytest.mark.asyncio
async def test_consume_nonce(nonce_store):
    """Test consuming a nonce."""
    nonce = "test_nonce_0"
    expires_at = 9999999999

    consumed = await nonce_store.consume(nonce, expires_at)
    assert consumed is True

    # Try to consume again — should fail
    consumed_again = await nonce_store.consume(nonce, expires_at)
    assert consumed_again is False


@pytest.mark.asyncio
async def test_is_consumed(nonce_store):
    """Test checking if nonce is consumed."""
    nonce = "test_nonce_1"
    expires_at = 9999999999

    assert not await nonce_store.is_consumed(nonce)

    await nonce_store.consume(nonce, expires_at)

    assert await nonce_store.is_consumed(nonce)


@pytest.mark.asyncio
async def test_purge_expired(nonce_store):
    """Test purging expired nonces."""
    import time

    nonce_old = "old_nonce"
    nonce_new = "new_nonce"

    # Consume old nonce with past expiration
    await nonce_store.consume(nonce_old, int(time.time()) - 10)

    # Consume new nonce with future expiration
    await nonce_store.consume(nonce_new, int(time.time()) + 3600)

    # Purge expired
    await nonce_store.purge_expired()

    # Old nonce should be gone
    assert not await nonce_store.is_consumed(nonce_old)

    # New nonce should still exist
    assert await nonce_store.is_consumed(nonce_new)

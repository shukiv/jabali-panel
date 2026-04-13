"""Persistent nonce store (SEC-REV-1)."""

import os
import time

import aiosqlite


class NonceStore:
    """
    SQLite-backed persistent nonce store.

    Prevents token replay across daemon restarts by storing consumed nonces
    with their expiration times. Uses WAL mode + FULL synchronous + fsync.
    """

    def __init__(self, db_path: str):
        """Initialize nonce store."""
        self.db_path = db_path
        self._db: aiosqlite.Connection | None = None

    async def init(self) -> None:
        """Initialize database (WAL mode, FULL sync, create table)."""
        # Ensure parent directory exists
        os.makedirs(os.path.dirname(self.db_path) or ".", exist_ok=True)

        self._db = await aiosqlite.connect(self.db_path)

        # Enable WAL mode for crash-safety and concurrent readers
        await self._db.execute("PRAGMA journal_mode=WAL")

        # Enable FULL synchronous mode: fsync after every write
        await self._db.execute("PRAGMA synchronous=FULL")

        # Create nonces table if it doesn't exist
        await self._db.execute(
            """
            CREATE TABLE IF NOT EXISTS nonces (
                nonce TEXT PRIMARY KEY,
                expires_at INTEGER NOT NULL
            )
            """
        )

        await self._db.commit()

        # Clean up expired nonces on startup (older than 60s to be safe)
        await self.purge_expired()

    async def close(self) -> None:
        """Close database connection."""
        if self._db:
            await self._db.close()

    async def consume(self, nonce: str, expires_at: int) -> bool:
        """
        Mark nonce as consumed.

        Returns: True if nonce was successfully consumed (wasn't already used)
        Raises: Exception on database error

        CRITICAL: Must fsync before returning to prevent replay if daemon crashes.
        """
        if not self._db:
            raise RuntimeError("NonceStore not initialized")

        try:
            # Insert nonce; if it already exists, UNIQUE constraint fails
            await self._db.execute(
                "INSERT INTO nonces (nonce, expires_at) VALUES (?, ?)",
                (nonce, expires_at),
            )

            # Commit and fsync to ensure persistence before returning
            await self._db.commit()

            # Explicit fsync on the database file
            # (PRAGMA synchronous=FULL should handle this, but explicit for clarity)
            if self._db:
                db_fd = os.open(self.db_path, os.O_RDONLY)
                try:
                    os.fsync(db_fd)
                finally:
                    os.close(db_fd)

            return True

        except aiosqlite.IntegrityError:
            # Nonce already consumed (UNIQUE constraint violation)
            return False

    async def is_consumed(self, nonce: str) -> bool:
        """Check if nonce has been consumed."""
        if not self._db:
            raise RuntimeError("NonceStore not initialized")

        cursor = await self._db.execute(
            "SELECT 1 FROM nonces WHERE nonce = ?",
            (nonce,),
        )
        row = await cursor.fetchone()
        await cursor.close()
        return row is not None

    async def purge_expired(self) -> None:
        """Remove expired nonces (expires_at < now)."""
        if not self._db:
            raise RuntimeError("NonceStore not initialized")

        now = int(time.time())
        await self._db.execute(
            "DELETE FROM nonces WHERE expires_at < ?",
            (now,),
        )
        await self._db.commit()

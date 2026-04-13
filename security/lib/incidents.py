"""SQLite incident store for persistent incident tracking."""

from __future__ import annotations

import json
import logging
from pathlib import Path

import aiosqlite

from lib.models import Incident, QuarantineRecord

logger = logging.getLogger(__name__)

_SCHEMA = """
CREATE TABLE IF NOT EXISTS incidents (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    username TEXT,
    event_type TEXT NOT NULL,
    total_score INTEGER NOT NULL,
    severity TEXT NOT NULL,
    action_taken TEXT NOT NULL,
    findings_json TEXT NOT NULL,
    file_event_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    resolved INTEGER NOT NULL DEFAULT 0,
    notes TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_incidents_user ON incidents(username);
CREATE INDEX IF NOT EXISTS idx_incidents_severity ON incidents(severity);
CREATE INDEX IF NOT EXISTS idx_incidents_created ON incidents(created_at);

CREATE TABLE IF NOT EXISTS quarantine (
    id TEXT PRIMARY KEY,
    incident_id TEXT,
    original_path TEXT NOT NULL,
    quarantine_path TEXT NOT NULL,
    username TEXT,
    sha256 TEXT NOT NULL,
    reason TEXT NOT NULL,
    created_at TEXT NOT NULL,
    restored INTEGER NOT NULL DEFAULT 0,
    deleted INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS blocked_ips (
    ip TEXT PRIMARY KEY,
    reason TEXT NOT NULL,
    blocked_at TEXT NOT NULL,
    expires_at TEXT,
    blocked_by TEXT DEFAULT 'auto'
);

CREATE TABLE IF NOT EXISTS waf_events (
    id TEXT PRIMARY KEY,
    client_ip TEXT NOT NULL,
    uri TEXT NOT NULL,
    method TEXT NOT NULL,
    rule_id INTEGER,
    rule_msg TEXT,
    severity TEXT,
    action TEXT,
    hostname TEXT,
    username TEXT,
    matched_data TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_waf_events_ip ON waf_events(client_ip);
CREATE INDEX IF NOT EXISTS idx_waf_events_created ON waf_events(created_at);

CREATE TABLE IF NOT EXISTS cleanup_records (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    strategy TEXT NOT NULL,
    success INTEGER NOT NULL,
    backup_path TEXT,
    changes_json TEXT,
    error TEXT,
    username TEXT,
    created_at TEXT NOT NULL
);
"""


class IncidentStore:
    def __init__(self, db_path: Path) -> None:
        self._db_path = db_path
        self._db: aiosqlite.Connection | None = None

    async def open(self) -> None:
        """Open database and create schema."""
        self._db_path.parent.mkdir(parents=True, exist_ok=True)
        self._db = await aiosqlite.connect(str(self._db_path))
        await self._db.executescript(_SCHEMA)
        await self._db.commit()

    async def close(self) -> None:
        if self._db:
            await self._db.close()
            self._db = None

    async def save(self, incident: Incident) -> None:
        """Insert or update an incident."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        await self._db.execute(
            """INSERT OR REPLACE INTO incidents
               (id, path, username, event_type, total_score, severity, action_taken,
                findings_json, file_event_json, created_at, resolved, notes)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                incident.id,
                incident.file_event.path,
                incident.username,
                incident.file_event.event_type,
                incident.total_score,
                incident.severity,
                incident.action_taken,
                json.dumps([f.model_dump() for f in incident.findings]),
                incident.file_event.model_dump_json(),
                incident.timestamp.isoformat(),
                int(incident.resolved),
                incident.notes,
            ),
        )
        await self._db.commit()

    async def save_quarantine(self, record: QuarantineRecord) -> None:
        """Insert a quarantine record."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        await self._db.execute(
            """INSERT OR REPLACE INTO quarantine
               (id, incident_id, original_path, quarantine_path, username, sha256,
                reason, created_at, restored, deleted)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                record.id,
                record.incident_id,
                record.original_path,
                record.quarantine_path,
                record.username,
                record.sha256,
                record.reason,
                record.timestamp.isoformat(),
                int(record.restored),
                int(record.deleted),
            ),
        )
        await self._db.commit()

    async def get(self, incident_id: str) -> Incident | None:
        """Get a single incident by ID."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        async with self._db.execute(
            "SELECT * FROM incidents WHERE id = ?", (incident_id,)
        ) as cursor:
            row = await cursor.fetchone()
            if not row:
                return None
            return self._row_to_incident(row, cursor.description)

    async def list_incidents(
        self,
        limit: int = 50,
        username: str | None = None,
        severity: str | None = None,
        since: str | None = None,
    ) -> list[Incident]:
        """List incidents with optional filters."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        conditions: list[str] = []
        params: list[str | int] = []

        if username:
            conditions.append("username = ?")
            params.append(username)
        if severity:
            conditions.append("severity = ?")
            params.append(severity)
        if since:
            conditions.append("created_at >= ?")
            params.append(since)

        where = " AND ".join(conditions)
        query = "SELECT * FROM incidents"
        if where:
            query += " WHERE " + where
        query += " ORDER BY created_at DESC LIMIT ?"
        params.append(limit)

        results: list[Incident] = []
        async with self._db.execute(query, params) as cursor:
            async for row in cursor:
                incident = self._row_to_incident(row, cursor.description)
                if incident:
                    results.append(incident)
        return results

    async def count_recent(self, hours: int = 24) -> int:
        """Count incidents in the last N hours."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        async with self._db.execute(
            "SELECT COUNT(*) FROM incidents WHERE created_at >= datetime('now', ?)",
            ("-%d hours" % hours,),
        ) as cursor:
            row = await cursor.fetchone()
            return row[0] if row else 0

    async def resolve(self, incident_id: str, notes: str = "") -> bool:
        """Mark an incident as resolved."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        cursor = await self._db.execute(
            "UPDATE incidents SET resolved = 1, notes = ? WHERE id = ?",
            (notes, incident_id),
        )
        await self._db.commit()
        return cursor.rowcount > 0

    async def list_quarantine(self, username: str | None = None) -> list[QuarantineRecord]:
        """List quarantine records."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        if username:
            query = "SELECT * FROM quarantine WHERE username = ? AND deleted = 0 ORDER BY created_at DESC"
            params: tuple = (username,)
        else:
            query = "SELECT * FROM quarantine WHERE deleted = 0 ORDER BY created_at DESC"
            params = ()

        results: list[QuarantineRecord] = []
        async with self._db.execute(query, params) as cursor:
            async for row in cursor:
                record = self._row_to_quarantine(row, cursor.description)
                if record:
                    results.append(record)
        return results

    async def get_quarantine_by_id(self, record_id: str) -> QuarantineRecord | None:
        """Fetch a single quarantine record by ID (O(1) primary-key lookup)."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        async with self._db.execute(
            "SELECT * FROM quarantine WHERE id = ? AND deleted = 0", (record_id,)
        ) as cursor:
            row = await cursor.fetchone()
            if row:
                return self._row_to_quarantine(row, cursor.description)
        return None

    async def mark_quarantine_restored(self, record_id: str) -> bool:
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        cursor = await self._db.execute(
            "UPDATE quarantine SET restored = 1 WHERE id = ?", (record_id,)
        )
        await self._db.commit()
        return cursor.rowcount > 0

    async def mark_quarantine_deleted(self, record_id: str) -> bool:
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        cursor = await self._db.execute(
            "UPDATE quarantine SET deleted = 1 WHERE id = ?", (record_id,)
        )
        await self._db.commit()
        return cursor.rowcount > 0

    async def save_cleanup(self, result) -> None:
        """Save a cleanup result."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        await self._db.execute(
            """INSERT OR REPLACE INTO cleanup_records
               (id, path, strategy, success, backup_path, changes_json, error, username, created_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (result.id, result.path, result.strategy, int(result.success),
             result.backup_path, json.dumps(result.changes_made),
             result.error, result.username, result.timestamp.isoformat()),
        )
        await self._db.commit()

    async def list_cleanups(self, limit: int = 50) -> list[dict]:
        """List recent cleanup records."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        results = []
        async with self._db.execute(
            "SELECT * FROM cleanup_records ORDER BY created_at DESC LIMIT ?", (limit,)
        ) as cursor:
            cols = [d[0] for d in cursor.description]
            async for row in cursor:
                results.append(dict(zip(cols, row)))
        return results

    # -- Public accessors for blocked_ips / waf_events / user stats ----------
    # These replace direct _db access from api/routes.py, daemon/server.py, etc.

    async def get_blocked_ips(self) -> list[dict]:
        """Return all blocked IPs ordered by blocked_at descending."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        results: list[dict] = []
        async with self._db.execute(
            "SELECT ip, reason, blocked_at, expires_at, blocked_by "
            "FROM blocked_ips ORDER BY blocked_at DESC"
        ) as cursor:
            async for row in cursor:
                results.append({
                    "ip": row[0],
                    "reason": row[1],
                    "blocked_at": row[2],
                    "expires_at": row[3],
                    "blocked_by": row[4],
                })
        return results

    async def save_blocked_ip(
        self, ip: str, reason: str, blocked_at: str, expires_at: str | None, blocked_by: str,
    ) -> None:
        """Insert or replace a blocked IP record."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        await self._db.execute(
            "INSERT OR REPLACE INTO blocked_ips (ip, reason, blocked_at, expires_at, blocked_by) "
            "VALUES (?, ?, ?, ?, ?)",
            (ip, reason, blocked_at, expires_at, blocked_by),
        )
        await self._db.commit()

    async def delete_blocked_ip(self, ip: str) -> bool:
        """Delete a blocked IP. Returns True if a row was deleted."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        cursor = await self._db.execute(
            "DELETE FROM blocked_ips WHERE ip = ?", (ip,)
        )
        await self._db.commit()
        return cursor.rowcount > 0

    async def save_waf_event(self, event) -> None:
        """Persist a single WAF event."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        await self._db.execute(
            "INSERT OR IGNORE INTO waf_events "
            "(id, client_ip, uri, method, rule_id, rule_msg, severity, action, "
            "hostname, username, matched_data, created_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                event.id,
                event.client_ip,
                event.uri,
                event.method,
                event.rule_id,
                event.rule_msg,
                event.severity,
                event.action,
                event.hostname,
                event.username,
                event.matched_data,
                event.timestamp.isoformat(),
            ),
        )
        await self._db.commit()

    async def get_waf_events(
        self,
        limit: int = 50,
        ip: str | None = None,
        rule_id: int | None = None,
        since: str | None = None,
    ) -> list[dict]:
        """Query waf_events with optional filters."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        conditions: list[str] = []
        params: list[str | int] = []

        if ip is not None:
            conditions.append("client_ip = ?")
            params.append(ip)
        if rule_id is not None:
            conditions.append("rule_id = ?")
            params.append(rule_id)
        if since is not None:
            conditions.append("created_at >= ?")
            params.append(since)

        query = (
            "SELECT id, client_ip, uri, method, rule_id, rule_msg, severity, "
            "action, hostname, username, matched_data, created_at FROM waf_events"
        )
        where = " AND ".join(conditions)
        if where:
            query += " WHERE " + where
        query += " ORDER BY created_at DESC LIMIT ?"
        params.append(limit)

        results: list[dict] = []
        async with self._db.execute(query, params) as cursor:
            async for row in cursor:
                results.append({
                    "id": row[0],
                    "client_ip": row[1],
                    "uri": row[2],
                    "method": row[3],
                    "rule_id": row[4],
                    "rule_msg": row[5],
                    "severity": row[6],
                    "action": row[7],
                    "hostname": row[8],
                    "username": row[9],
                    "matched_data": row[10],
                    "created_at": row[11],
                })
        return results

    async def get_waf_stats(self) -> dict:
        """Return WAF aggregation stats for the last 24 hours."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")

        async with self._db.execute(
            "SELECT COUNT(*) FROM waf_events WHERE created_at >= datetime('now', '-24 hours')"
        ) as cursor:
            row = await cursor.fetchone()
            total_24h = row[0] if row else 0

        async with self._db.execute(
            "SELECT COUNT(*) FROM waf_events "
            "WHERE created_at >= datetime('now', '-24 hours') AND action = 'deny'"
        ) as cursor:
            row = await cursor.fetchone()
            blocked_24h = row[0] if row else 0

        top_ips: list[dict] = []
        async with self._db.execute(
            "SELECT client_ip, COUNT(*) AS cnt FROM waf_events "
            "WHERE created_at >= datetime('now', '-24 hours') "
            "GROUP BY client_ip ORDER BY cnt DESC LIMIT 10"
        ) as cursor:
            async for row in cursor:
                top_ips.append({"ip": row[0], "count": row[1]})

        top_rules: list[dict] = []
        async with self._db.execute(
            "SELECT rule_id, rule_msg, COUNT(*) AS cnt FROM waf_events "
            "WHERE created_at >= datetime('now', '-24 hours') AND rule_id > 0 "
            "GROUP BY rule_id ORDER BY cnt DESC LIMIT 10"
        ) as cursor:
            async for row in cursor:
                top_rules.append({"rule_id": row[0], "rule_msg": row[1], "count": row[2]})

        top_uris: list[dict] = []
        async with self._db.execute(
            "SELECT uri, COUNT(*) AS cnt FROM waf_events "
            "WHERE created_at >= datetime('now', '-24 hours') "
            "GROUP BY uri ORDER BY cnt DESC LIMIT 10"
        ) as cursor:
            async for row in cursor:
                top_uris.append({"uri": row[0], "count": row[1]})

        return {
            "total_events_24h": total_24h,
            "blocked_24h": blocked_24h,
            "top_ips": top_ips,
            "top_rules": top_rules,
            "top_uris": top_uris,
        }

    async def get_user_stats(self) -> list[dict]:
        """Return per-user incident stats (username, count, max_score)."""
        if self._db is None:
            raise RuntimeError("IncidentStore not initialized — call open() first")
        results: list[dict] = []
        async with self._db.execute(
            "SELECT username, COUNT(*) AS count, MAX(total_score) AS max_score "
            "FROM incidents WHERE username IS NOT NULL GROUP BY username "
            "ORDER BY count DESC"
        ) as cursor:
            async for row in cursor:
                results.append({
                    "username": row[0],
                    "incident_count": row[1],
                    "max_score": row[2],
                })
        return results

    @staticmethod
    def _row_to_incident(row, description) -> Incident | None:
        """Convert a database row to an Incident model."""
        try:
            cols = [d[0] for d in description]
            data = dict(zip(cols, row))
            from lib.models import FileEvent, Finding

            file_event = FileEvent.model_validate_json(data["file_event_json"])
            findings = [Finding.model_validate(f) for f in json.loads(data["findings_json"])]
            from datetime import datetime

            # Parse stored timestamp — don't generate a new one
            ts = data.get("created_at", "")
            try:
                timestamp = datetime.fromisoformat(ts)
            except (ValueError, TypeError):
                timestamp = None

            return Incident(
                id=data["id"],
                file_event=file_event,
                findings=findings,
                total_score=data["total_score"],
                severity=data["severity"],
                action_taken=data["action_taken"],
                **({"timestamp": timestamp} if timestamp else {}),
                username=data["username"],
                resolved=bool(data["resolved"]),
                notes=data["notes"] or "",
            )
        except Exception:
            logger.exception("Failed to deserialize incident row")
            return None

    @staticmethod
    def _row_to_quarantine(row, description) -> QuarantineRecord | None:
        """Convert a database row to a QuarantineRecord model."""
        try:
            cols = [d[0] for d in description]
            data = dict(zip(cols, row))

            # Parse stored timestamp — don't generate a new one
            timestamp = None
            if data.get("created_at"):
                try:
                    from datetime import datetime
                    timestamp = datetime.fromisoformat(data["created_at"])
                except (ValueError, TypeError):
                    pass

            return QuarantineRecord(
                id=data["id"],
                original_path=data["original_path"],
                quarantine_path=data["quarantine_path"],
                username=data["username"],
                reason=data["reason"],
                incident_id=data["incident_id"],
                sha256=data["sha256"],
                restored=bool(data["restored"]),
                deleted=bool(data["deleted"]),
                **({"timestamp": timestamp} if timestamp else {}),
            )
        except Exception:
            logger.exception("Failed to deserialize quarantine row")
            return None

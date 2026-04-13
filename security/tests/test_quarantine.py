"""Tests for lib.quarantine.QuarantineManager — file isolation and restore."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

import pytest

from lib.models import FileEvent, Finding, Incident
from lib.quarantine import QuarantineManager


@pytest.fixture
def quarantine_dir(tmp_path):
    q = tmp_path / "quarantine"
    q.mkdir()
    return q


@pytest.fixture
def manager(quarantine_dir, tmp_path):
    # tmp_path lives under /tmp/pytest-of-<user>/... which is NOT in the
    # production restore allow-list (/home/, /var/www/). We inject it here
    # so the restore test can round-trip a file without needing /tmp/pytest
    # to leak into production code.
    return QuarantineManager(
        base_dir=str(quarantine_dir),
        restore_allowlist=("/home/", "/var/www/", str(tmp_path)),
    )


@pytest.fixture
def sample_incident():
    event = FileEvent(
        event_type="create",
        path="/home/user/public_html/malicious.php",
        username="user",
        size=100,
    )
    findings = [Finding(scanner="heuristic", rule="shell_exec", score=40, description="test")]
    return Incident(
        file_event=event,
        findings=findings,
        total_score=70,
        severity="high",
        action_taken="quarantine",
        username="user",
    )


class TestQuarantineFile:
    async def test_moves_file_to_quarantine(self, manager, tmp_path, sample_incident):
        src = tmp_path / "malicious.php"
        src.write_bytes(b"<?php system('whoami'); ?>")
        sample_incident.file_event.path = str(src)
        record = await manager.quarantine_file(str(src), sample_incident)
        assert record is not None
        assert not src.exists()  # original removed
        assert Path(record.quarantine_path).exists()

    async def test_rejects_symlinks(self, manager, tmp_path, sample_incident):
        target = tmp_path / "real.php"
        target.write_text("real")
        link = tmp_path / "link.php"
        link.symlink_to(target)
        sample_incident.file_event.path = str(link)
        record = await manager.quarantine_file(str(link), sample_incident)
        assert record is None
        assert target.exists()  # target NOT moved

    async def test_writes_metadata_sidecar(self, manager, tmp_path, sample_incident):
        src = tmp_path / "malicious.php"
        src.write_bytes(b"malware")
        sample_incident.file_event.path = str(src)
        record = await manager.quarantine_file(str(src), sample_incident)
        meta = Path(record.quarantine_path + ".meta.json")
        assert meta.exists()
        data = json.loads(meta.read_text())
        assert data["sha256"]
        assert data["original_path"] == str(src)

    async def test_computes_sha256(self, manager, tmp_path, sample_incident):
        content = b"known content for hashing"
        src = tmp_path / "file.php"
        src.write_bytes(content)
        sample_incident.file_event.path = str(src)
        record = await manager.quarantine_file(str(src), sample_incident)
        expected = hashlib.sha256(content).hexdigest()
        assert record.sha256 == expected

    async def test_handles_name_collision(self, manager, tmp_path, sample_incident):
        src1 = tmp_path / "malicious.php"
        src1.write_bytes(b"first")
        sample_incident.file_event.path = str(src1)
        r1 = await manager.quarantine_file(str(src1), sample_incident)
        # Create another file with same name
        src2 = tmp_path / "malicious.php"
        src2.write_bytes(b"second")
        sample_incident.file_event.path = str(src2)
        r2 = await manager.quarantine_file(str(src2), sample_incident)
        assert r1 is not None and r2 is not None
        assert r1.quarantine_path != r2.quarantine_path

    async def test_nonexistent_file_returns_none(self, manager, sample_incident):
        record = await manager.quarantine_file("/nonexistent/file.php", sample_incident)
        assert record is None


class TestRestoreFile:
    async def test_restore_moves_back(self, manager, tmp_path, sample_incident):
        src = tmp_path / "file.php"
        src.write_bytes(b"content")
        original_path = str(src)
        sample_incident.file_event.path = original_path
        record = await manager.quarantine_file(original_path, sample_incident)
        assert not Path(original_path).exists()
        ok = await manager.restore_file(record)
        assert ok
        assert Path(original_path).exists()


class TestDeleteQuarantined:
    async def test_delete_removes_file_and_meta(self, manager, tmp_path, sample_incident):
        src = tmp_path / "file.php"
        src.write_bytes(b"content")
        sample_incident.file_event.path = str(src)
        record = await manager.quarantine_file(str(src), sample_incident)
        ok = await manager.delete_quarantined(record)
        assert ok
        assert not Path(record.quarantine_path).exists()


class TestCountAndList:
    async def test_count_after_quarantine(self, manager, tmp_path, sample_incident):
        src = tmp_path / "file.php"
        src.write_bytes(b"x")
        sample_incident.file_event.path = str(src)
        await manager.quarantine_file(str(src), sample_incident)
        count = await manager.count()
        assert count >= 1

    async def test_list_records(self, manager, tmp_path, sample_incident):
        src = tmp_path / "file.php"
        src.write_bytes(b"x")
        sample_incident.file_event.path = str(src)
        await manager.quarantine_file(str(src), sample_incident)
        records = await manager.list_records()
        assert len(records) >= 1

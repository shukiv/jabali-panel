"""Tests for lib.models — Pydantic data models."""

from __future__ import annotations

from datetime import datetime, timezone

from lib.models import FileEvent, Finding, Incident, QuarantineRecord, ThreatScore


class TestFileEvent:
    def test_auto_generates_id(self) -> None:
        event = FileEvent(event_type="create", path="/home/alice/test.php")
        assert event.id is not None
        assert len(event.id) == 12

    def test_auto_generates_timestamp(self) -> None:
        event = FileEvent(event_type="create", path="/home/alice/test.php")
        assert isinstance(event.timestamp, datetime)
        assert event.timestamp.tzinfo == timezone.utc

    def test_unique_ids(self) -> None:
        e1 = FileEvent(event_type="create", path="/a.php")
        e2 = FileEvent(event_type="create", path="/b.php")
        assert e1.id != e2.id

    def test_default_values(self) -> None:
        event = FileEvent(event_type="modify", path="/test.php")
        assert event.username is None
        assert event.size == 0
        assert event.in_uploads_dir is False

    def test_explicit_values(self) -> None:
        event = FileEvent(
            event_type="create",
            path="/home/bob/uploads/shell.php",
            username="bob",
            size=1024,
            in_uploads_dir=True,
        )
        assert event.username == "bob"
        assert event.size == 1024
        assert event.in_uploads_dir is True


class TestThreatScore:
    def test_valid_actions(self) -> None:
        for action in ("ignore", "log", "quarantine", "suspend"):
            ts = ThreatScore(total=0, findings=[], action=action)
            assert ts.action == action

    def test_findings_list(self) -> None:
        f1 = Finding(scanner="test", rule="r1", score=10, description="d1")
        f2 = Finding(scanner="test", rule="r2", score=20, description="d2")
        ts = ThreatScore(total=30, findings=[f1, f2], action="log")
        assert len(ts.findings) == 2
        assert ts.total == 30


class TestIncidentSummary:
    def test_summary_returns_top_3_findings(self) -> None:
        findings = [
            Finding(scanner="s", rule="r1", score=10, description="First"),
            Finding(scanner="s", rule="r2", score=20, description="Second"),
            Finding(scanner="s", rule="r3", score=30, description="Third"),
            Finding(scanner="s", rule="r4", score=40, description="Fourth"),
        ]
        event = FileEvent(event_type="create", path="/test.php")
        incident = Incident(
            file_event=event,
            findings=findings,
            total_score=100,
            severity="critical",
            action_taken="quarantine",
        )
        summary = incident.summary
        assert "First" in summary
        assert "Second" in summary
        assert "Third" in summary
        assert "Fourth" not in summary

    def test_summary_with_fewer_than_3_findings(self) -> None:
        findings = [
            Finding(scanner="s", rule="r1", score=10, description="Only one"),
        ]
        event = FileEvent(event_type="modify", path="/test.php")
        incident = Incident(
            file_event=event,
            findings=findings,
            total_score=10,
            severity="low",
            action_taken="log",
        )
        assert incident.summary == "Only one"

    def test_summary_separator(self) -> None:
        findings = [
            Finding(scanner="s", rule="r1", score=10, description="A"),
            Finding(scanner="s", rule="r2", score=20, description="B"),
        ]
        event = FileEvent(event_type="modify", path="/test.php")
        incident = Incident(
            file_event=event,
            findings=findings,
            total_score=30,
            severity="medium",
            action_taken="log",
        )
        assert incident.summary == "A | B"


class TestQuarantineRecord:
    def test_required_fields(self) -> None:
        record = QuarantineRecord(
            original_path="/home/alice/shell.php",
            quarantine_path="/var/security/quarantine/abc123",
            reason="Malicious webshell",
            sha256="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
        )
        assert record.original_path == "/home/alice/shell.php"
        assert record.sha256 == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        assert record.restored is False
        assert record.deleted is False

    def test_auto_generates_id_and_timestamp(self) -> None:
        record = QuarantineRecord(
            original_path="/test.php",
            quarantine_path="/quarantine/test",
            reason="test",
            sha256="abc123",
        )
        assert record.id is not None
        assert len(record.id) == 16
        assert isinstance(record.timestamp, datetime)

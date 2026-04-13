"""Tests for lib.behavior_tracker — BehaviorTracker."""

from __future__ import annotations

from lib.behavior_tracker import BehaviorTracker
from lib.models import FileEvent


def _make_event(
    path: str = "/home/alice/public_html/test.php",
    event_type: str = "create",
    username: str | None = "alice",
    in_uploads: bool = False,
) -> FileEvent:
    return FileEvent(
        event_type=event_type,
        path=path,
        username=username,
        in_uploads_dir=in_uploads,
    )


class TestRapidCreateModify:
    async def test_rapid_create_modify_generates_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        path = "/home/alice/public_html/shell.php"

        # Create event
        await tracker.record_event(_make_event(path=path, event_type="create"))
        # Immediate modify (same monotonic window)
        findings = await tracker.record_event(_make_event(path=path, event_type="modify"))

        rules = [f.rule for f in findings]
        assert "rapid_create_modify" in rules

    async def test_no_finding_without_create_first(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        path = "/home/alice/public_html/existing.php"

        # Only modify, no create
        findings = await tracker.record_event(_make_event(path=path, event_type="modify"))
        rules = [f.rule for f in findings]
        assert "rapid_create_modify" not in rules


class TestNewFileInUploads:
    async def test_new_file_in_uploads_generates_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = await tracker.record_event(
            _make_event(
                path="/home/alice/public_html/wp-content/uploads/shell.php",
                event_type="create",
                in_uploads=True,
            )
        )
        rules = [f.rule for f in findings]
        assert "new_file_in_uploads" in rules

    async def test_modify_in_uploads_no_uploads_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = await tracker.record_event(
            _make_event(
                path="/home/alice/public_html/wp-content/uploads/image.php",
                event_type="modify",
                in_uploads=True,
            )
        )
        rules = [f.rule for f in findings]
        assert "new_file_in_uploads" not in rules


class TestRandomFilename:
    async def test_random_hex_filename_generates_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = await tracker.record_event(
            _make_event(
                path="/home/alice/public_html/a1b2c3d4e5f6.php",
                event_type="create",
            )
        )
        rules = [f.rule for f in findings]
        assert "random_filename" in rules

    async def test_normal_filename_no_random_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = await tracker.record_event(
            _make_event(
                path="/home/alice/public_html/index.php",
                event_type="create",
            )
        )
        rules = [f.rule for f in findings]
        assert "random_filename" not in rules


class TestSuspiciousFilename:
    async def test_suspicious_name_generates_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        for name in ("shell.php", "backdoor.php", "c99.php", "wso.php"):
            tracker = BehaviorTracker(ttl=300)
            findings = await tracker.record_event(
                _make_event(
                    path=f"/home/alice/public_html/{name}",
                    event_type="create",
                )
            )
            rules = [f.rule for f in findings]
            assert "suspicious_filename" in rules, f"Expected suspicious_filename for {name}"


class TestBurstCreation:
    async def test_burst_creation_over_100_generates_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = []
        for i in range(105):
            findings = await tracker.record_event(
                _make_event(
                    path=f"/home/alice/public_html/file_{i}.php",
                    event_type="create",
                    username="alice",
                )
            )
        rules = [f.rule for f in findings]
        assert "burst_file_creation" in rules

    async def test_under_100_files_no_burst_finding(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = []
        for i in range(90):
            findings = await tracker.record_event(
                _make_event(
                    path=f"/home/alice/public_html/file_{i}.php",
                    event_type="create",
                    username="alice",
                )
            )
        rules = [f.rule for f in findings]
        assert "burst_file_creation" not in rules


class TestNormalCreation:
    async def test_single_normal_file_no_findings(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = await tracker.record_event(
            _make_event(
                path="/home/alice/public_html/about.php",
                event_type="create",
            )
        )
        # A normal filename, not in uploads, single creation => no behavior findings
        assert findings == []

    async def test_modify_event_no_behavioral_findings(self) -> None:
        tracker = BehaviorTracker(ttl=300)
        findings = await tracker.record_event(
            _make_event(
                path="/home/alice/public_html/index.php",
                event_type="modify",
            )
        )
        assert findings == []

"""Tests for lib.scoring — ScoringEngine."""

from __future__ import annotations

from lib.config import JabaliConfig
from lib.models import FileEvent, Finding
from lib.scoring import ScoringEngine


def _make_finding(score: int, rule: str = "test_rule") -> Finding:
    return Finding(
        scanner="test",
        rule=rule,
        score=score,
        description=f"test finding score={score}",
    )


def _make_event(path: str = "/home/alice/public_html/index.php", in_uploads: bool = False) -> FileEvent:
    return FileEvent(
        event_type="modify",
        path=path,
        username="alice",
        in_uploads_dir=in_uploads,
    )


class TestScoringEngine:
    def setup_method(self) -> None:
        self.config = JabaliConfig(score_log=40, score_quarantine=70, score_suspend=100)
        self.engine = ScoringEngine(self.config)

    def test_empty_findings_returns_ignore(self) -> None:
        event = _make_event()
        result = self.engine.evaluate(event, [])
        assert result.total == 0
        assert result.action == "ignore"
        assert result.findings == []

    def test_score_below_40_returns_ignore(self) -> None:
        event = _make_event()
        findings = [_make_finding(20)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 20
        assert result.action == "ignore"

    def test_score_40_returns_log(self) -> None:
        event = _make_event()
        findings = [_make_finding(40)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 40
        assert result.action == "log"

    def test_score_69_returns_log(self) -> None:
        event = _make_event()
        findings = [_make_finding(69)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 69
        assert result.action == "log"

    def test_score_70_returns_quarantine(self) -> None:
        event = _make_event()
        findings = [_make_finding(70)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 70
        assert result.action == "quarantine"

    def test_score_99_returns_quarantine(self) -> None:
        event = _make_event()
        findings = [_make_finding(99)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 99
        assert result.action == "quarantine"

    def test_score_100_returns_suspend(self) -> None:
        event = _make_event()
        findings = [_make_finding(100)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 100
        assert result.action == "suspend"

    def test_score_above_100_returns_suspend(self) -> None:
        event = _make_event()
        findings = [_make_finding(50), _make_finding(80)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 130
        assert result.action == "suspend"

    def test_uploads_dir_multiplier(self) -> None:
        event = _make_event(in_uploads=True)
        findings = [_make_finding(40)]
        result = self.engine.evaluate(event, findings)
        # 40 * 1.5 = 60
        assert result.total == 60
        assert result.action == "log"

    def test_uploads_dir_multiplier_pushes_to_quarantine(self) -> None:
        event = _make_event(in_uploads=True)
        findings = [_make_finding(50)]
        result = self.engine.evaluate(event, findings)
        # 50 * 1.5 = 75
        assert result.total == 75
        assert result.action == "quarantine"

    def test_multiple_findings_summed(self) -> None:
        event = _make_event()
        findings = [_make_finding(20), _make_finding(15), _make_finding(10)]
        result = self.engine.evaluate(event, findings)
        assert result.total == 45
        assert result.action == "log"
        assert len(result.findings) == 3


class TestSeverityFromScore:
    def test_critical(self) -> None:
        assert ScoringEngine.severity_from_score(100) == "critical"
        assert ScoringEngine.severity_from_score(150) == "critical"

    def test_high(self) -> None:
        assert ScoringEngine.severity_from_score(70) == "high"
        assert ScoringEngine.severity_from_score(99) == "high"

    def test_medium(self) -> None:
        assert ScoringEngine.severity_from_score(40) == "medium"
        assert ScoringEngine.severity_from_score(69) == "medium"

    def test_low(self) -> None:
        assert ScoringEngine.severity_from_score(0) == "low"
        assert ScoringEngine.severity_from_score(39) == "low"

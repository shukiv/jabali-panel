"""Regression tests for bugs found during audit."""

from __future__ import annotations

from pathlib import Path

from lib.config import _safe_int, load_config


class TestChallengConfigBounds:
    """Bug: challenge_difficulty and challenge_ttl used raw int() instead of
    _safe_int(), allowing invalid values (0, negative, non-numeric)."""

    def test_challenge_difficulty_default(self, tmp_path: Path) -> None:
        config = load_config(tmp_path / "nonexistent.conf")
        assert config.challenge_difficulty == 18

    def test_challenge_ttl_default(self, tmp_path: Path) -> None:
        config = load_config(tmp_path / "nonexistent.conf")
        assert config.challenge_ttl == 86400

    def test_challenge_difficulty_invalid_string_uses_default(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('CHALLENGE_DIFFICULTY="not_a_number"\n', encoding="utf-8")
        config = load_config(conf)
        # Should fallback to default 18, not crash with ValueError
        assert config.challenge_difficulty == 18

    def test_challenge_ttl_invalid_string_uses_default(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('CHALLENGE_TTL="abc"\n', encoding="utf-8")
        config = load_config(conf)
        # Should fallback to default 86400, not crash with ValueError
        assert config.challenge_ttl == 86400

    def test_challenge_difficulty_zero_clamped_to_min(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('CHALLENGE_DIFFICULTY="0"\n', encoding="utf-8")
        config = load_config(conf)
        assert config.challenge_difficulty >= 1

    def test_challenge_difficulty_negative_clamped_to_min(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('CHALLENGE_DIFFICULTY="-5"\n', encoding="utf-8")
        config = load_config(conf)
        assert config.challenge_difficulty >= 1

    def test_challenge_ttl_zero_clamped_to_min(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('CHALLENGE_TTL="0"\n', encoding="utf-8")
        config = load_config(conf)
        assert config.challenge_ttl >= 1

    def test_challenge_difficulty_huge_clamped_to_max(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('CHALLENGE_DIFFICULTY="999"\n', encoding="utf-8")
        config = load_config(conf)
        assert config.challenge_difficulty <= 64


class TestQueryParamSafeInt:
    """Bug: API routes used raw int() on query params, crashing with ValueError
    on non-numeric input instead of returning 400."""

    def test_safe_int_non_numeric_returns_default(self) -> None:
        assert _safe_int("abc", 50) == 50

    def test_safe_int_empty_string_returns_default(self) -> None:
        assert _safe_int("", 50) == 50

    def test_safe_int_clamps_min(self) -> None:
        assert _safe_int("-5", 50, min_val=1) == 1

    def test_safe_int_clamps_max(self) -> None:
        assert _safe_int("9999", 50, max_val=500) == 500


class TestScanSymlinkRace:
    """Bug: scanning.py checks is_symlink() then reads with read_bytes(),
    allowing TOCTOU race. Should use O_NOFOLLOW for the read."""

    def test_read_rejects_symlink_via_nofollow(self, tmp_path: Path) -> None:
        """Verify that _safe_read_bytes uses O_NOFOLLOW to reject symlinks
        atomically (no TOCTOU gap)."""
        from lib.safe_io import safe_read_bytes

        target = tmp_path / "real.txt"
        target.write_bytes(b"secret content")
        link = tmp_path / "link.txt"
        link.symlink_to(target)

        # Should raise OSError (ELOOP) when trying to open a symlink with O_NOFOLLOW
        import pytest
        with pytest.raises(OSError):
            safe_read_bytes(str(link))

    def test_read_normal_file_works(self, tmp_path: Path) -> None:
        from lib.safe_io import safe_read_bytes

        target = tmp_path / "normal.txt"
        target.write_bytes(b"hello world")

        result = safe_read_bytes(str(target))
        assert result == b"hello world"

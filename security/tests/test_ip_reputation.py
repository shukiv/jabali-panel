"""Tests for lib.threat_intel.ip_reputation — IPReputationDB."""

from __future__ import annotations

from pathlib import Path

from lib.threat_intel.ip_reputation import IPReputationDB


class TestLoadFeed:
    def test_load_feed_from_file(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text("1.2.3.4\n5.6.7.8\n", encoding="utf-8")
        db = IPReputationDB()
        count = db.load_feed("test_feed", feed_file)
        assert count == 2

    def test_comments_and_blanks_skipped(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text(
            "# This is a comment\n"
            "; Another comment\n"
            "\n"
            "1.2.3.4\n"
            "  \n"
            "5.6.7.8\n",
            encoding="utf-8",
        )
        db = IPReputationDB()
        count = db.load_feed("test_feed", feed_file)
        assert count == 2

    def test_missing_file_returns_zero(self, tmp_path: Path) -> None:
        db = IPReputationDB()
        count = db.load_feed("missing", tmp_path / "nonexistent.txt")
        assert count == 0


class TestPlainIPLookup:
    def test_ip_in_feed_found(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text("1.2.3.4\n10.0.0.1\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("blocklist", feed_file)
        matches = db.check("1.2.3.4")
        assert "blocklist" in matches

    def test_is_malicious_returns_true(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text("1.2.3.4\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("blocklist", feed_file)
        assert db.is_malicious("1.2.3.4") is True


class TestCIDRLookup:
    def test_ip_in_cidr_range_found(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text("192.168.1.0/24\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("cidr_feed", feed_file)
        matches = db.check("192.168.1.100")
        assert "cidr_feed" in matches

    def test_ip_outside_cidr_not_found(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text("192.168.1.0/24\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("cidr_feed", feed_file)
        matches = db.check("192.168.2.1")
        assert matches == []


class TestIPNotInAnyFeed:
    def test_unknown_ip_returns_empty_list(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text("1.2.3.4\n10.0.0.0/8\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("blocklist", feed_file)
        matches = db.check("203.0.113.50")
        assert matches == []

    def test_is_malicious_returns_false(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "blocklist.txt"
        feed_file.write_text("1.2.3.4\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("blocklist", feed_file)
        assert db.is_malicious("203.0.113.50") is False


class TestMultipleFeeds:
    def test_returns_all_matching_feed_names(self, tmp_path: Path) -> None:
        feed_a = tmp_path / "feed_a.txt"
        feed_a.write_text("1.2.3.4\n", encoding="utf-8")
        feed_b = tmp_path / "feed_b.txt"
        feed_b.write_text("1.2.3.0/24\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("alpha", feed_a)
        db.load_feed("bravo", feed_b)
        matches = db.check("1.2.3.4")
        assert "alpha" in matches
        assert "bravo" in matches
        assert len(matches) == 2


class TestSpamhausFormat:
    def test_ip_with_semicolon_comment_parsed(self, tmp_path: Path) -> None:
        feed_file = tmp_path / "drop.txt"
        feed_file.write_text(
            "; Spamhaus DROP list\n"
            "1.2.3.0/24 ; SBL000001\n"
            "5.6.7.0/24 ; SBL000002\n",
            encoding="utf-8",
        )
        db = IPReputationDB()
        count = db.load_feed("spamhaus", feed_file)
        assert count == 2
        matches = db.check("1.2.3.100")
        assert "spamhaus" in matches


class TestProperties:
    def test_total_entries(self, tmp_path: Path) -> None:
        feed_a = tmp_path / "a.txt"
        feed_a.write_text("1.2.3.4\n5.6.7.8\n", encoding="utf-8")
        feed_b = tmp_path / "b.txt"
        feed_b.write_text("10.0.0.0/8\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("a", feed_a)
        db.load_feed("b", feed_b)
        assert db.total_entries == 3

    def test_feed_names(self, tmp_path: Path) -> None:
        feed_a = tmp_path / "a.txt"
        feed_a.write_text("1.2.3.4\n", encoding="utf-8")
        feed_b = tmp_path / "b.txt"
        feed_b.write_text("5.6.7.8\n", encoding="utf-8")
        db = IPReputationDB()
        db.load_feed("alpha", feed_a)
        db.load_feed("bravo", feed_b)
        assert sorted(db.feed_names) == ["alpha", "bravo"]

    def test_empty_db(self) -> None:
        db = IPReputationDB()
        assert db.total_entries == 0
        assert db.feed_names == []
        assert db.check("1.2.3.4") == []

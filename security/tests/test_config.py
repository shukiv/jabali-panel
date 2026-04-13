"""Tests for lib.config — parsing, loading, and helpers."""

from __future__ import annotations

from pathlib import Path

from lib.config import JabaliConfig, _bool, _safe_int, load_config, parse_conf, update_conf_key


class TestParseConf:
    def test_reads_key_value(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('MY_KEY="my_value"\n', encoding="utf-8")
        result = parse_conf(conf)
        assert result == {"MY_KEY": "my_value"}

    def test_skips_comments(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text(
            '# This is a comment\n'
            'KEY1="val1"\n'
            '# Another comment\n'
            'KEY2="val2"\n',
            encoding="utf-8",
        )
        result = parse_conf(conf)
        assert result == {"KEY1": "val1", "KEY2": "val2"}

    def test_skips_blank_lines(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text(
            '\n'
            'KEY1="val1"\n'
            '\n'
            '   \n'
            'KEY2="val2"\n',
            encoding="utf-8",
        )
        result = parse_conf(conf)
        assert result == {"KEY1": "val1", "KEY2": "val2"}

    def test_unquoted_value(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('LOG_LEVEL=debug\n', encoding="utf-8")
        result = parse_conf(conf)
        assert result == {"LOG_LEVEL": "debug"}

    def test_single_quoted_value(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text("MY_KEY='hello world'\n", encoding="utf-8")
        result = parse_conf(conf)
        assert result == {"MY_KEY": "hello world"}

    def test_nonexistent_file_returns_empty(self, tmp_path: Path) -> None:
        conf = tmp_path / "missing.conf"
        result = parse_conf(conf)
        assert result == {}

    def test_multiple_keys(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text(
            'A="1"\n'
            'B="2"\n'
            'C="3"\n',
            encoding="utf-8",
        )
        result = parse_conf(conf)
        assert len(result) == 3
        assert result["A"] == "1"
        assert result["B"] == "2"
        assert result["C"] == "3"


class TestLoadConfig:
    def test_defaults_when_no_file(self, tmp_path: Path) -> None:
        conf = tmp_path / "nonexistent.conf"
        config = load_config(conf)
        assert isinstance(config, JabaliConfig)
        assert config.score_log == 40
        assert config.score_quarantine == 70
        assert config.score_suspend == 100
        assert config.workers == 4
        assert config.log_level == "info"

    def test_overrides_from_file(self, tmp_path: Path) -> None:
        conf = tmp_path / "jabali.conf"
        conf.write_text(
            'LOG_LEVEL="debug"\n'
            'WORKERS="8"\n',
            encoding="utf-8",
        )
        config = load_config(conf)
        assert config.log_level == "debug"
        assert config.workers == 8

    def test_scan_extensions_default(self, tmp_path: Path) -> None:
        conf = tmp_path / "nonexistent.conf"
        config = load_config(conf)
        assert ".php" in config.scan_extensions
        assert ".js" in config.scan_extensions

    def test_skip_dirs_default(self, tmp_path: Path) -> None:
        conf = tmp_path / "nonexistent.conf"
        config = load_config(conf)
        assert ".git" in config.skip_dirs
        assert "node_modules" in config.skip_dirs


class TestUpdateConfKey:
    def test_creates_file_if_missing(self, tmp_path: Path) -> None:
        conf = tmp_path / "new.conf"
        update_conf_key(conf, "MY_KEY", "my_val")
        assert conf.exists()
        result = parse_conf(conf)
        assert result["MY_KEY"] == "my_val"

    def test_updates_existing_key(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('MY_KEY="old_val"\n', encoding="utf-8")
        update_conf_key(conf, "MY_KEY", "new_val")
        result = parse_conf(conf)
        assert result["MY_KEY"] == "new_val"

    def test_preserves_other_keys(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text(
            'KEY_A="aaa"\n'
            'KEY_B="bbb"\n',
            encoding="utf-8",
        )
        update_conf_key(conf, "KEY_A", "updated")
        result = parse_conf(conf)
        assert result["KEY_A"] == "updated"
        assert result["KEY_B"] == "bbb"

    def test_appends_new_key(self, tmp_path: Path) -> None:
        conf = tmp_path / "test.conf"
        conf.write_text('EXISTING="val"\n', encoding="utf-8")
        update_conf_key(conf, "NEW_KEY", "new_val")
        result = parse_conf(conf)
        assert result["EXISTING"] == "val"
        assert result["NEW_KEY"] == "new_val"

    def test_roundtrip_write_read(self, tmp_path: Path) -> None:
        conf = tmp_path / "roundtrip.conf"
        update_conf_key(conf, "ALPHA", "hello")
        update_conf_key(conf, "BETA", "world")
        result = parse_conf(conf)
        assert result["ALPHA"] == "hello"
        assert result["BETA"] == "world"


class TestBoolHelper:
    def test_yes_is_true(self) -> None:
        assert _bool("yes") is True

    def test_true_is_true(self) -> None:
        assert _bool("true") is True

    def test_one_is_true(self) -> None:
        assert _bool("1") is True

    def test_on_is_true(self) -> None:
        assert _bool("on") is True

    def test_no_is_false(self) -> None:
        assert _bool("no") is False

    def test_false_is_false(self) -> None:
        assert _bool("false") is False

    def test_zero_is_false(self) -> None:
        assert _bool("0") is False

    def test_empty_is_false(self) -> None:
        assert _bool("") is False

    def test_case_insensitive(self) -> None:
        assert _bool("YES") is True
        assert _bool("True") is True


class TestSafeInt:
    def test_valid_int(self) -> None:
        assert _safe_int("42", default=0) == 42

    def test_invalid_returns_default(self) -> None:
        assert _safe_int("abc", default=10) == 10

    def test_min_clamping(self) -> None:
        assert _safe_int("0", default=5, min_val=1) == 1

    def test_max_clamping(self) -> None:
        assert _safe_int("999", default=5, max_val=100) == 100

    def test_within_range(self) -> None:
        assert _safe_int("50", default=0, min_val=1, max_val=100) == 50

    def test_empty_string_returns_default(self) -> None:
        assert _safe_int("", default=7) == 7

    def test_negative_number(self) -> None:
        assert _safe_int("-5", default=0, min_val=0) == 0

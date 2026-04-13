"""Tests for lib.filter — PreFilter."""

from __future__ import annotations

from pathlib import Path

from lib.config import JabaliConfig
from lib.filter import PreFilter


def _make_filter(config: JabaliConfig | None = None) -> PreFilter:
    if config is None:
        config = JabaliConfig()
    return PreFilter(config)


class TestPreFilter:
    def test_php_file_should_scan(self, tmp_path: Path) -> None:
        f = tmp_path / "test.php"
        f.write_text("<?php echo 'hello'; ?>")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is True

    def test_png_file_should_not_scan(self, tmp_path: Path) -> None:
        f = tmp_path / "image.png"
        f.write_bytes(b"\x89PNG\r\n\x1a\n" + b"\x00" * 100)
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

    def test_git_dir_should_not_scan(self, tmp_path: Path) -> None:
        git_dir = tmp_path / ".git"
        git_dir.mkdir()
        f = git_dir / "config.php"
        f.write_text("<?php // git internal ?>")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

    def test_node_modules_should_not_scan(self, tmp_path: Path) -> None:
        nm_dir = tmp_path / "node_modules"
        nm_dir.mkdir()
        f = nm_dir / "package.php"
        f.write_text("<?php // node module ?>")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

    def test_empty_file_should_not_scan(self, tmp_path: Path) -> None:
        f = tmp_path / "empty.php"
        f.write_text("")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

    def test_file_exceeding_max_size_should_not_scan(self, tmp_path: Path) -> None:
        f = tmp_path / "big.php"
        # Default max_file_size is 2097152 (2 MB)
        f.write_bytes(b"A" * (2097152 + 1))
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

    def test_file_within_max_size_should_scan(self, tmp_path: Path) -> None:
        f = tmp_path / "small.php"
        f.write_text("<?php echo 1; ?>")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is True

    def test_missing_file_should_not_scan(self) -> None:
        pf = _make_filter()
        assert pf.should_scan("/nonexistent/path/file.php") is False

    def test_vendor_dir_should_not_scan(self, tmp_path: Path) -> None:
        vendor_dir = tmp_path / "vendor"
        vendor_dir.mkdir()
        f = vendor_dir / "lib.php"
        f.write_text("<?php class Lib {} ?>")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

    def test_js_extension_should_scan(self, tmp_path: Path) -> None:
        f = tmp_path / "app.js"
        f.write_text("console.log('hi');")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is True

    def test_txt_extension_should_not_scan(self, tmp_path: Path) -> None:
        f = tmp_path / "readme.txt"
        f.write_text("just text")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

    def test_pycache_dir_should_not_scan(self, tmp_path: Path) -> None:
        cache_dir = tmp_path / "__pycache__"
        cache_dir.mkdir()
        f = cache_dir / "module.py"
        f.write_text("x = 1")
        pf = _make_filter()
        assert pf.should_scan(str(f)) is False

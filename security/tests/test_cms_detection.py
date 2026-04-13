"""Tests for lib.cleanup.cms_cleaner — detect_cms and find_cms_root."""

from __future__ import annotations

from pathlib import Path

from lib.cleanup.cms_cleaner import CMSCleaner


class TestDetectCMSWordPress:
    def test_directory_with_wp_config(self, tmp_path: Path) -> None:
        (tmp_path / "wp-config.php").write_text("<?php // WP config", encoding="utf-8")
        assert CMSCleaner.detect_cms(str(tmp_path)) == "wordpress"

    def test_file_inside_wp_root(self, tmp_path: Path) -> None:
        (tmp_path / "wp-config.php").write_text("<?php // WP config", encoding="utf-8")
        theme_dir = tmp_path / "wp-content" / "themes" / "mytheme"
        theme_dir.mkdir(parents=True)
        test_file = theme_dir / "index.php"
        test_file.write_text("<?php // theme", encoding="utf-8")
        assert CMSCleaner.detect_cms(str(test_file)) == "wordpress"


class TestDetectCMSJoomla:
    def test_directory_with_configuration_and_administrator(self, tmp_path: Path) -> None:
        (tmp_path / "configuration.php").write_text("<?php // Joomla config", encoding="utf-8")
        (tmp_path / "administrator").mkdir()
        assert CMSCleaner.detect_cms(str(tmp_path)) == "joomla"

    def test_configuration_without_administrator_not_joomla(self, tmp_path: Path) -> None:
        (tmp_path / "configuration.php").write_text("<?php // config", encoding="utf-8")
        # No administrator/ directory — should not match joomla
        assert CMSCleaner.detect_cms(str(tmp_path)) is None


class TestDetectCMSNone:
    def test_empty_directory(self, tmp_path: Path) -> None:
        assert CMSCleaner.detect_cms(str(tmp_path)) is None

    def test_random_files(self, tmp_path: Path) -> None:
        (tmp_path / "index.html").write_text("<html>Hello</html>", encoding="utf-8")
        (tmp_path / "style.css").write_text("body {}", encoding="utf-8")
        assert CMSCleaner.detect_cms(str(tmp_path)) is None


class TestFindCMSRoot:
    def test_finds_wordpress_root(self, tmp_path: Path) -> None:
        (tmp_path / "wp-config.php").write_text("<?php // WP config", encoding="utf-8")
        root = CMSCleaner.find_cms_root(str(tmp_path))
        assert root == tmp_path

    def test_finds_joomla_root(self, tmp_path: Path) -> None:
        (tmp_path / "configuration.php").write_text("<?php // Joomla", encoding="utf-8")
        root = CMSCleaner.find_cms_root(str(tmp_path))
        assert root == tmp_path

    def test_nested_path_walks_up(self, tmp_path: Path) -> None:
        (tmp_path / "wp-config.php").write_text("<?php // WP config", encoding="utf-8")
        nested = tmp_path / "wp-content" / "plugins" / "myplugin"
        nested.mkdir(parents=True)
        test_file = nested / "plugin.php"
        test_file.write_text("<?php // plugin", encoding="utf-8")
        root = CMSCleaner.find_cms_root(str(test_file))
        assert root == tmp_path

    def test_no_cms_root_returns_none(self, tmp_path: Path) -> None:
        nested = tmp_path / "a" / "b" / "c"
        nested.mkdir(parents=True)
        root = CMSCleaner.find_cms_root(str(nested))
        assert root is None

    def test_stops_after_max_levels(self, tmp_path: Path) -> None:
        # Create a deeply nested path (> 5 levels) with CMS root far above
        deep = tmp_path / "a" / "b" / "c" / "d" / "e" / "f" / "g"
        deep.mkdir(parents=True)
        (tmp_path / "wp-config.php").write_text("<?php", encoding="utf-8")
        # 7 levels deep — should not find the root (max 5 levels walk-up)
        root = CMSCleaner.find_cms_root(str(deep))
        assert root is None

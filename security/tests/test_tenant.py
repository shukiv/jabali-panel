"""Tests for lib.tenant — resolve_user."""

from __future__ import annotations

from lib.tenant import resolve_user


class TestResolveUser:
    def test_home_alice_public_html(self) -> None:
        assert resolve_user("/home/alice/public_html/x.php") == "alice"

    def test_var_www_returns_none(self) -> None:
        assert resolve_user("/var/www/x.php") is None

    def test_home_bob_tmp(self) -> None:
        assert resolve_user("/home/bob/tmp/file.sh") == "bob"

    def test_etc_passwd_returns_none(self) -> None:
        assert resolve_user("/etc/passwd") is None

    def test_home_root_level_user(self) -> None:
        assert resolve_user("/home/charlie/file.txt") == "charlie"

    def test_home_with_deep_nesting(self) -> None:
        assert resolve_user("/home/dave/a/b/c/d/e.php") == "dave"

    def test_empty_string(self) -> None:
        assert resolve_user("") is None

    def test_relative_path(self) -> None:
        assert resolve_user("home/alice/file.php") is None

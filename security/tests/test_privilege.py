"""Tests for lib.privilege — sudo helpers for privilege separation."""

from __future__ import annotations

from unittest.mock import patch


class TestSudoPrefix:
    def test_root_returns_empty(self) -> None:
        with patch("lib.privilege._IS_ROOT", True):
            from lib.privilege import sudo_prefix
            assert sudo_prefix() == []

    def test_nonroot_returns_sudo(self) -> None:
        with patch("lib.privilege._IS_ROOT", False):
            from lib.privilege import sudo_prefix
            result = sudo_prefix()
            assert len(result) == 1
            assert "sudo" in result[0]


class TestSudoCmd:
    def test_root_passthrough(self) -> None:
        with patch("lib.privilege._IS_ROOT", True):
            from lib.privilege import sudo_cmd
            assert sudo_cmd("/usr/bin/systemctl", "reload", "nginx") == [
                "/usr/bin/systemctl", "reload", "nginx",
            ]

    def test_nonroot_prepends_sudo(self) -> None:
        with patch("lib.privilege._IS_ROOT", False):
            from lib.privilege import sudo_cmd
            result = sudo_cmd("/usr/bin/systemctl", "reload", "nginx")
            assert result[0].endswith("sudo")
            assert result[1:] == ["/usr/bin/systemctl", "reload", "nginx"]


class TestIsServiceUser:
    def test_root_is_service(self) -> None:
        with patch("lib.privilege._IS_ROOT", True):
            from lib.privilege import is_service_user
            assert is_service_user() is True

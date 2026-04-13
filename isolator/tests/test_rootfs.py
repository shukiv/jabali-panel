"""Tests for rootfs builder."""

from __future__ import annotations

from unittest.mock import patch

import pytest

from jabali_isolator.rootfs import create_rootfs, destroy_rootfs, rootfs_exists


class TestCreateRootfs:
    def test_creates_directory_structure(self, isolator_dirs, fake_user):
        with patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user):
            root = create_rootfs("testuser")

        assert root == isolator_dirs["machines"] / "testuser-php"
        assert (root / "etc" / "passwd").is_file()
        assert (root / "etc" / "group").is_file()
        assert (root / "etc" / "resolv.conf").is_file()
        assert (root / "tmp").is_dir()
        assert (root / "home" / "testuser").is_dir()
        assert (root / "usr").is_dir()
        assert (root / "lib").is_dir()
        assert (root / "bin").is_dir()
        assert (root / "var" / "log").is_dir()

    def test_passwd_contains_root_and_user(self, isolator_dirs, fake_user):
        with patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user):
            root = create_rootfs("testuser")

        passwd = (root / "etc" / "passwd").read_text()
        lines = passwd.strip().splitlines()
        assert len(lines) == 2
        assert lines[0].startswith("root:")
        assert lines[1].startswith("testuser:")
        assert ":1001:1001:" in lines[1]

    def test_group_contains_root_wwwdata_and_user_group(self, isolator_dirs, fake_user):
        with (
            patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user),
            patch("jabali_isolator.rootfs._lookup_group", side_effect=KeyError),
        ):
            root = create_rootfs("testuser")

        group = (root / "etc" / "group").read_text()
        lines = group.strip().splitlines()
        assert len(lines) == 3
        assert lines[0].startswith("root:")
        assert lines[1] == "www-data:x:33:"
        assert lines[2].startswith("testuser:")

    def test_idempotent_rebuild(self, isolator_dirs, fake_user):
        with patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user):
            root1 = create_rootfs("testuser")
            root2 = create_rootfs("testuser")

        assert root1 == root2
        assert (root2 / "etc" / "passwd").is_file()

    def test_nonexistent_user_raises(self, isolator_dirs):
        with patch("jabali_isolator.rootfs._lookup_user", side_effect=KeyError("testuser")):
            with pytest.raises(KeyError):
                create_rootfs("testuser")


class TestDestroyRootfs:
    def test_removes_existing(self, isolator_dirs, fake_user):
        with patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user):
            create_rootfs("testuser")

        assert destroy_rootfs("testuser") is True
        assert not (isolator_dirs["machines"] / "testuser-php").exists()

    def test_returns_false_when_missing(self, isolator_dirs):
        assert destroy_rootfs("nonexistent") is False


class TestRootfsExists:
    def test_true_when_exists(self, isolator_dirs, fake_user):
        with patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user):
            create_rootfs("testuser")

        assert rootfs_exists("testuser") is True

    def test_false_when_missing(self, isolator_dirs):
        assert rootfs_exists("nonexistent") is False

"""Tests for systemd unit file generators."""

from __future__ import annotations

from jabali_isolator.units import (
    generate_nspawn_unit,
    generate_service_dropin,
    remove_unit_files,
    unit_files_exist,
    write_nspawn_unit,
    write_service_dropin,
)


class TestGenerateNspawnUnit:
    def test_contains_bind_mounts(self):
        content = generate_nspawn_unit("testuser")
        assert "BindReadOnly=/usr" in content
        assert "BindReadOnly=/lib" in content
        assert "BindReadOnly=/etc/php" in content
        assert "Bind=/home/testuser" in content

    def test_uses_sleep_infinity(self):
        content = generate_nspawn_unit("testuser")
        assert "Parameters=/bin/sleep infinity" in content

    def test_no_fpm_socket_bind(self):
        content = generate_nspawn_unit("testuser")
        assert "/run/jabali-fpm" not in content
        assert "pool.d" not in content

    def test_tmpfs(self):
        content = generate_nspawn_unit("testuser")
        assert "TemporaryFileSystem=/tmp:mode=1777" in content

    def test_no_virtual_ethernet(self):
        content = generate_nspawn_unit("testuser")
        assert "VirtualEthernet=no" in content

    def test_exec_section(self):
        content = generate_nspawn_unit("testuser")
        assert "PrivateUsers=no" in content
        assert "Boot=no" in content
        assert "ProcessTwo=yes" in content

    def test_different_users_produce_different_units(self):
        a = generate_nspawn_unit("alice")
        b = generate_nspawn_unit("bob")
        assert "/home/alice" in a
        assert "/home/bob" in b
        assert "/home/alice" not in b


class TestGenerateServiceDropin:
    def test_defaults(self):
        content = generate_service_dropin()
        assert "MemoryMax=256M" in content
        assert "CPUQuota=100%" in content

    def test_custom_values(self):
        content = generate_service_dropin(memory="1G", cpu="200%")
        assert "MemoryMax=1G" in content
        assert "CPUQuota=200%" in content


class TestWriteNspawnUnit:
    def test_writes_file(self, isolator_dirs):
        path = write_nspawn_unit("testuser")
        assert path.name == "testuser-php.nspawn"
        assert path.is_file()
        assert "BindReadOnly=/usr" in path.read_text()


class TestWriteServiceDropin:
    def test_writes_dropin(self, isolator_dirs):
        path = write_service_dropin("testuser", memory="256M", cpu="50%")
        assert path.name == "limits.conf"
        assert "MemoryMax=256M" in path.read_text()
        assert "CPUQuota=50%" in path.read_text()


class TestRemoveUnitFiles:
    def test_removes_both(self, isolator_dirs):
        write_nspawn_unit("testuser")
        write_service_dropin("testuser")

        remove_unit_files("testuser")
        assert not (isolator_dirs["nspawn"] / "testuser-php.nspawn").exists()
        assert not (isolator_dirs["system"] / "systemd-nspawn@testuser-php.service.d").exists()

    def test_tolerates_missing(self, isolator_dirs):
        remove_unit_files("nonexistent")


class TestUnitFilesExist:
    def test_true_when_exists(self, isolator_dirs):
        write_nspawn_unit("testuser")
        assert unit_files_exist("testuser") is True

    def test_false_when_missing(self, isolator_dirs):
        assert unit_files_exist("nonexistent") is False

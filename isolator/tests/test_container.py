"""Tests for container manager."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import pytest

from jabali_isolator.container import (
    IsolatorError,
    _validate_user,
    create,
    destroy,
    is_available,
    list_all,
    restart,
    start,
    status,
    stop,
)


class TestValidateUser:
    def test_valid_usernames(self):
        for name in ["user1", "test_user", "john.doe", "a-b", "user123"]:
            _validate_user(name)

    def test_rejects_empty(self):
        with pytest.raises(IsolatorError):
            _validate_user("")

    def test_rejects_shell_metacharacters(self):
        for bad in ["user;rm", "user$(cmd)", "user`cmd`", "../etc", "user name", "USER"]:
            with pytest.raises(IsolatorError):
                _validate_user(bad)

    def test_rejects_starting_with_number(self):
        with pytest.raises(IsolatorError):
            _validate_user("1user")


class TestIsAvailable:
    def test_true_when_installed(self):
        with patch("jabali_isolator.container.shutil.which", return_value="/usr/bin/systemd-nspawn"):
            assert is_available() is True

    def test_false_when_missing(self):
        with patch("jabali_isolator.container.shutil.which", return_value=None):
            assert is_available() is False


class TestCreate:
    @pytest.mark.asyncio
    async def test_creates_container(self, isolator_dirs, fake_user):
        with (
            patch("jabali_isolator.container.is_available", return_value=True),
            patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user),
            patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")),
        ):
            result = await create("testuser", memory="256M", cpu="50%")

        assert result["user"] == "testuser"
        assert result["memory"] == "256M"
        assert result["cpu"] == "50%"
        assert (isolator_dirs["machines"] / "testuser-php" / "etc" / "passwd").is_file()
        assert (isolator_dirs["nspawn"] / "testuser-php.nspawn").is_file()
        nspawn_content = (isolator_dirs["nspawn"] / "testuser-php.nspawn").read_text()
        assert "sleep infinity" in nspawn_content
        assert "php-fpm" not in nspawn_content

    @pytest.mark.asyncio
    async def test_no_systemctl_enable(self, isolator_dirs, fake_user):
        """Containers should NOT auto-start on boot."""
        with (
            patch("jabali_isolator.container.is_available", return_value=True),
            patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user),
            patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")) as mock_run,
        ):
            await create("testuser")

        enable_calls = [c for c in mock_run.call_args_list if "enable" in c[0][0]]
        assert len(enable_calls) == 0

    @pytest.mark.asyncio
    async def test_fails_without_nspawn(self):
        with patch("jabali_isolator.container.is_available", return_value=False):
            with pytest.raises(IsolatorError, match="not installed"):
                await create("testuser")

    @pytest.mark.asyncio
    async def test_fails_for_nonexistent_user(self, isolator_dirs):
        with (
            patch("jabali_isolator.container.is_available", return_value=True),
            patch("jabali_isolator.rootfs._lookup_user", side_effect=KeyError("nope")),
        ):
            with pytest.raises(IsolatorError, match="does not exist"):
                await create("testuser")

    @pytest.mark.asyncio
    async def test_rejects_invalid_memory(self, isolator_dirs):
        with patch("jabali_isolator.container.is_available", return_value=True):
            with pytest.raises(IsolatorError, match="Invalid memory"):
                await create("testuser", memory="banana")

    @pytest.mark.asyncio
    async def test_rejects_invalid_cpu(self, isolator_dirs):
        with patch("jabali_isolator.container.is_available", return_value=True):
            with pytest.raises(IsolatorError, match="Invalid CPU"):
                await create("testuser", cpu="fast")

    @pytest.mark.asyncio
    async def test_rejects_invalid_username(self):
        with pytest.raises(IsolatorError, match="Invalid username"):
            await create("bad;user")


class TestDestroy:
    @pytest.mark.asyncio
    async def test_destroys_existing(self, isolator_dirs, fake_user):
        with (
            patch("jabali_isolator.container.is_available", return_value=True),
            patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user),
            patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")),
        ):
            await create("testuser")

        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")):
            removed = await destroy("testuser")

        assert removed is True
        assert not (isolator_dirs["machines"] / "testuser-php").exists()
        assert not (isolator_dirs["nspawn"] / "testuser-php.nspawn").exists()

    @pytest.mark.asyncio
    async def test_succeeds_when_disable_fails(self, isolator_dirs, fake_user):
        with (
            patch("jabali_isolator.container.is_available", return_value=True),
            patch("jabali_isolator.rootfs._lookup_user", return_value=fake_user),
            patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")),
        ):
            await create("testuser")

        with patch(
            "jabali_isolator.container._run",
            new_callable=AsyncMock,
            side_effect=[
                (0, "", ""),                         # stop (tolerates not-running)
                (1, "", "Failed to disable unit"),   # disable fails
                (0, "", ""),                         # daemon-reload
            ],
        ):
            removed = await destroy("testuser")

        assert removed is True
        assert not (isolator_dirs["machines"] / "testuser-php").exists()

    @pytest.mark.asyncio
    async def test_returns_false_when_nothing_to_destroy(self, isolator_dirs):
        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(1, "", "not found")):
            removed = await destroy("nonexistent")

        assert removed is False


class TestStart:
    @pytest.mark.asyncio
    async def test_starts_existing(self, isolator_dirs):
        (isolator_dirs["machines"] / "testuser-php").mkdir(parents=True)

        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")):
            assert await start("testuser") is True

    @pytest.mark.asyncio
    async def test_fails_when_no_rootfs(self, isolator_dirs):
        with pytest.raises(IsolatorError, match="does not exist"):
            await start("nonexistent")


class TestStop:
    @pytest.mark.asyncio
    async def test_stops_running(self, isolator_dirs):
        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")):
            assert await stop("testuser") is True

    @pytest.mark.asyncio
    async def test_tolerates_not_running(self, isolator_dirs):
        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(1, "", "not running")):
            assert await stop("testuser") is True


class TestRestart:
    @pytest.mark.asyncio
    async def test_restart_succeeds(self, isolator_dirs):
        (isolator_dirs["machines"] / "testuser-php").mkdir(parents=True)

        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")):
            assert await restart("testuser") is True

    @pytest.mark.asyncio
    async def test_restart_fails_when_no_rootfs(self, isolator_dirs):
        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(0, "", "")):
            # stop() tolerates missing, but start() raises
            with pytest.raises(IsolatorError, match="does not exist"):
                await restart("nonexistent")


class TestStatus:
    @pytest.mark.asyncio
    async def test_running(self, isolator_dirs):
        (isolator_dirs["machines"] / "testuser-php").mkdir(parents=True)

        with patch(
            "jabali_isolator.container._run",
            new_callable=AsyncMock,
            side_effect=[
                (0, "State=running", ""),
                (0, "enabled", ""),
            ],
        ):
            info = await status("testuser")

        assert info["state"] == "running"
        assert info["exists"] is True
        assert info["enabled"] is True

    @pytest.mark.asyncio
    async def test_missing(self, isolator_dirs):
        info = await status("nonexistent")
        assert info["state"] == "missing"
        assert info["exists"] is False
        assert info["enabled"] is False

    @pytest.mark.asyncio
    async def test_stopped_and_disabled(self, isolator_dirs):
        (isolator_dirs["machines"] / "testuser-php").mkdir(parents=True)

        with patch(
            "jabali_isolator.container._run",
            new_callable=AsyncMock,
            side_effect=[
                (1, "", ""),
                (1, "disabled", ""),
            ],
        ):
            info = await status("testuser")

        assert info["state"] == "stopped"
        assert info["enabled"] is False


class TestListAll:
    @pytest.mark.asyncio
    async def test_lists_containers(self, isolator_dirs):
        (isolator_dirs["machines"] / "alice-php").mkdir(parents=True)
        (isolator_dirs["machines"] / "bob-php").mkdir(parents=True)
        (isolator_dirs["machines"] / "not-a-container").mkdir(parents=True)

        with patch("jabali_isolator.container._run", new_callable=AsyncMock, return_value=(1, "", "not found")):
            containers = await list_all()

        assert len(containers) == 2
        users = [c["user"] for c in containers]
        assert "alice" in users
        assert "bob" in users

    @pytest.mark.asyncio
    async def test_empty_when_no_machines_dir(self, isolator_dirs):
        # machines dir doesn't exist (not created by fixture)
        containers = await list_all()
        assert containers == []

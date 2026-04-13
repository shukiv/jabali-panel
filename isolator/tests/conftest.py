"""Shared test fixtures."""

from __future__ import annotations

import pwd

import pytest


@pytest.fixture
def isolator_dirs(tmp_path, monkeypatch):
    """Override all jabali_isolator paths to use tmp_path.

    Returns a dict of the temp directories for assertions.
    """
    import jabali_isolator.config as cfg

    monkeypatch.setattr(cfg, "MACHINES_DIR", str(tmp_path / "machines"))
    monkeypatch.setattr(cfg, "NSPAWN_DIR", str(tmp_path / "nspawn"))
    monkeypatch.setattr(cfg, "SERVICE_DROPIN_BASE", str(tmp_path / "system"))
    monkeypatch.setattr(cfg, "LOCK_DIR", str(tmp_path / "locks"))

    return {
        "machines": tmp_path / "machines",
        "nspawn": tmp_path / "nspawn",
        "system": tmp_path / "system",
        "locks": tmp_path / "locks",
        "root": tmp_path,
    }


@pytest.fixture
def fake_user():
    """Return a mock passwd entry for testuser."""
    return pwd.struct_passwd(("testuser", "x", 1001, 1001, "", "/home/testuser", "/bin/bash"))

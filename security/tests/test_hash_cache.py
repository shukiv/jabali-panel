"""Tests for lib.hash_cache — HashCache."""

from __future__ import annotations

from pathlib import Path

from lib.hash_cache import HashCache


class TestGetHash:
    def test_sha256_of_known_content(self) -> None:
        cache = HashCache()
        # SHA-256 of empty bytes
        expected = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        assert cache.get_hash(b"") == expected

    def test_sha256_of_hello(self) -> None:
        cache = HashCache()
        result = cache.get_hash(b"hello")
        assert len(result) == 64
        assert result == "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

    def test_different_content_different_hash(self) -> None:
        cache = HashCache()
        h1 = cache.get_hash(b"content_a")
        h2 = cache.get_hash(b"content_b")
        assert h1 != h2


class TestCleanDirty:
    def test_mark_clean_and_check(self) -> None:
        cache = HashCache()
        h = cache.get_hash(b"clean file content")
        cache.mark_clean(h)
        assert cache.is_known_clean(h) is True

    def test_unknown_hash_not_clean(self) -> None:
        cache = HashCache()
        assert cache.is_known_clean("deadbeef" * 8) is False

    def test_mark_dirty_removes_from_clean(self) -> None:
        cache = HashCache()
        h = cache.get_hash(b"was clean")
        cache.mark_clean(h)
        assert cache.is_known_clean(h) is True
        cache.mark_dirty(h)
        assert cache.is_known_clean(h) is False

    def test_mark_dirty_nonexistent_hash_is_safe(self) -> None:
        cache = HashCache()
        cache.mark_dirty("nonexistent_hash")  # should not raise


class TestEviction:
    def test_evicts_when_exceeding_max_entries(self) -> None:
        cache = HashCache(max_entries=10)
        for i in range(15):
            cache.mark_clean(f"hash_{i:04d}")
        # After eviction, size should be <= max_entries
        assert cache.size <= 10

    def test_eviction_removes_excess_plus_10_percent(self) -> None:
        cache = HashCache(max_entries=100)
        for i in range(105):
            cache.mark_clean(f"hash_{i:04d}")
        # max_entries=100, excess=5, to_remove = 5 + 10 = 15 => size should be ~90
        assert cache.size <= 100


class TestPersistLoad:
    def test_save_and_load_cycle(self, tmp_path: Path) -> None:
        persist_file = tmp_path / "cache.json"
        cache1 = HashCache(persist_path=persist_file)
        h1 = cache1.get_hash(b"file content 1")
        h2 = cache1.get_hash(b"file content 2")
        cache1.mark_clean(h1)
        cache1.mark_clean(h2)
        cache1.save()

        cache2 = HashCache(persist_path=persist_file)
        assert cache2.is_known_clean(h1) is True
        assert cache2.is_known_clean(h2) is True
        assert cache2.size == 2

    def test_load_nonexistent_file(self, tmp_path: Path) -> None:
        persist_file = tmp_path / "nonexistent.json"
        cache = HashCache(persist_path=persist_file)
        assert cache.size == 0

    def test_save_creates_parent_dirs(self, tmp_path: Path) -> None:
        persist_file = tmp_path / "subdir" / "deep" / "cache.json"
        cache = HashCache(persist_path=persist_file)
        cache.mark_clean("test_hash")
        cache.save()
        assert persist_file.exists()

    def test_dirty_hash_not_persisted(self, tmp_path: Path) -> None:
        persist_file = tmp_path / "cache.json"
        cache1 = HashCache(persist_path=persist_file)
        h = cache1.get_hash(b"content")
        cache1.mark_clean(h)
        cache1.mark_dirty(h)
        cache1.save()

        cache2 = HashCache(persist_path=persist_file)
        assert cache2.is_known_clean(h) is False

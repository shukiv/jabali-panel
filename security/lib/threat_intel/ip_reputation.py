"""IP reputation database -- memory-efficient CIDR blocklist lookups.

IPv4 entries are stored as parallel sorted lists of (start, end) integers.
Lookups use bisect for O(log n) performance instead of O(n) linear scan.
IPv6 entries use a set (these feeds are typically <5% IPv6).
"""
from __future__ import annotations

import bisect
import ipaddress
import logging
from pathlib import Path

logger = logging.getLogger(__name__)


class IPReputationDB:
    """In-memory IP reputation database loaded from blocklist files."""

    def __init__(self) -> None:
        # feed_name -> (starts, ends) parallel sorted lists of IPv4 int ranges
        self._v4_feeds: dict[str, tuple[list[int], list[int]]] = {}
        # feed_name -> set of IPv6Network objects (sparse)
        self._v6_feeds: dict[str, set[ipaddress.IPv6Network]] = {}
        # feed_name -> entry count (post-merge)
        self._entry_counts: dict[str, int] = {}

    def load_feed(self, name: str, path: Path) -> int:
        """Load a blocklist file (one IP or CIDR per line). Returns entry count."""
        if not path.is_file():
            logger.warning("Feed file not found: %s", path)
            return 0

        v4_ranges: list[tuple[int, int]] = []
        v6_nets: set[ipaddress.IPv6Network] = set()

        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line or line.startswith("#") or line.startswith(";"):
                continue
            # Handle "IP ; comment" format (Spamhaus DROP)
            if ";" in line:
                line = line.split(";")[0].strip()
            try:
                net = ipaddress.ip_network(line, strict=False)
                if isinstance(net, ipaddress.IPv4Network):
                    v4_ranges.append((
                        int(net.network_address),
                        int(net.broadcast_address),
                    ))
                else:
                    v6_nets.add(net)
            except ValueError:
                continue

        # Sort and merge overlapping ranges — reduces lookup set and memory
        v4_ranges.sort()
        v4_ranges = _merge_ranges(v4_ranges)

        starts = [s for s, _ in v4_ranges]
        ends = [e for _, e in v4_ranges]
        self._v4_feeds[name] = (starts, ends)
        self._v6_feeds[name] = v6_nets

        count = len(starts) + len(v6_nets)
        self._entry_counts[name] = count
        logger.info(
            "Loaded feed %s: %d v4 ranges + %d v6 nets from %s",
            name, len(starts), len(v6_nets), path,
        )
        return count

    def check(self, ip: str) -> list[str]:
        """Check if an IP is in any loaded feed. Returns list of matching feed names."""
        try:
            addr = ipaddress.ip_address(ip)
        except ValueError:
            return []

        matches: list[str] = []
        if isinstance(addr, ipaddress.IPv4Address):
            addr_int = int(addr)
            for name, (starts, ends) in self._v4_feeds.items():
                if _bisect_contains(starts, ends, addr_int):
                    matches.append(name)
        else:
            for name, v6_nets in self._v6_feeds.items():
                for net in v6_nets:
                    if addr in net:
                        matches.append(name)
                        break
        return matches

    def is_malicious(self, ip: str) -> bool:
        """Quick check: is this IP in any feed?"""
        return len(self.check(ip)) > 0

    @property
    def total_entries(self) -> int:
        return sum(self._entry_counts.values())

    @property
    def feed_names(self) -> list[str]:
        return list(self._v4_feeds.keys())


def _merge_ranges(ranges: list[tuple[int, int]]) -> list[tuple[int, int]]:
    """Merge overlapping or adjacent sorted integer ranges."""
    if not ranges:
        return []
    merged: list[tuple[int, int]] = [ranges[0]]
    for start, end in ranges[1:]:
        prev_start, prev_end = merged[-1]
        if start <= prev_end + 1:
            if end > prev_end:
                merged[-1] = (prev_start, end)
        else:
            merged.append((start, end))
    return merged


def _bisect_contains(starts: list[int], ends: list[int], addr: int) -> bool:
    """O(log n) range lookup: is addr contained in any (start, end) range?

    starts and ends must be parallel sorted lists (starts[i] <= ends[i] for all i,
    and starts is non-decreasing). After merging, no two ranges overlap.
    """
    if not starts:
        return False
    idx = bisect.bisect_right(starts, addr) - 1
    if idx < 0:
        return False
    return ends[idx] >= addr

# ADR-0002: Sorted Int Ranges with Bisect for IP Reputation

**Date**: 2026-04-02
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

The IP reputation database loads blocklist feeds (Spamhaus DROP, blocklist.de, Tor exit nodes) into memory for real-time lookups. Feeds like blocklist.de contain 500K+ individual IP entries. The original implementation stored each entry as a Python `ipaddress.IPv4Network` object in a `set`, consuming ~150MB for large feeds. Lookups were O(n) per feed — iterating every network to check containment.

On a busy server, every logged request triggers an IP check across all feeds. O(n) × 500K entries × multiple feeds is a significant CPU bottleneck.

## Decision

Store IPv4 entries as parallel sorted lists of `(start, end)` integer ranges. Merge overlapping/adjacent ranges at load time. Use `bisect.bisect_right` for O(log n) lookups. Keep IPv6 in a set (feeds are <5% IPv6).

## Alternatives Considered

### Alternative 1: Python ipaddress objects in a set (original)
- **Pros**: Simple, readable, standard library
- **Cons**: ~150MB for 500K entries, O(n) lookup per feed
- **Why not**: Unacceptable memory and CPU cost at scale

### Alternative 2: Radix trie / Patricia trie
- **Pros**: O(k) lookup (k = prefix length), purpose-built for CIDR
- **Cons**: Complex implementation, third-party dependency (py-radix), higher memory overhead per node
- **Why not**: Over-engineered for the use case; sorted ranges + bisect is simpler and sufficient

### Alternative 3: array.array of packed uint32
- **Pros**: ~4MB for 500K entries (vs ~30MB for Python int lists)
- **Cons**: Platform-dependent item size, less readable
- **Why not**: Can be added later if 30MB is still too much; Python int lists are good enough

## Consequences

### Positive
- Memory: ~150MB → ~30MB for large feeds (5x reduction)
- CPU: O(n) → O(log n) per lookup (orders of magnitude faster on 500K entries)
- Adjacent/overlapping ranges merged at load time, reducing entry count further

### Negative
- IPv6 still uses set-based O(n) lookup (acceptable — feeds are <5% IPv6)
- Range merging adds O(n log n) sort at load time (one-time cost, negligible)

### Risks
- Bisect on integer ranges assumes non-overlapping after merge — merge function must be correct. Covered by existing tests.

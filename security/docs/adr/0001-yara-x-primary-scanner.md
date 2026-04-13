# ADR-0001: YARA-X as Primary Scanner, No clamd Daemon

**Date**: 2026-04-02
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

Jabali Security needs real-time file scanning on shared hosting servers, many of which are small VPS boxes with 2GB RAM. ClamAV's daemon (`clamd`) loads the entire virus database into memory and keeps it resident, consuming ~950MB RSS. On a 2GB server this is 48% of total RAM, leaving insufficient memory for the hosting workload.

## Decision

Use YARA-X (Rust-based) as the primary real-time scanner. Install only the ClamAV CLI tools (`clamav` + `clamav-freshclam`) for virus definitions and manual `clamscan` use. Do not install `clamav-daemon`. The socket-based `ClamavScanner` auto-detects clamd if an admin installs it separately.

## Alternatives Considered

### Alternative 1: Run clamd permanently
- **Pros**: Fast per-file scans (DB already in memory), full virus coverage
- **Cons**: ~950MB RSS permanently, unacceptable on 2GB VPS
- **Why not**: Memory cost is prohibitive for the target environment

### Alternative 2: Start clamd on-demand, stop after scanning
- **Pros**: Full ClamAV coverage when needed, no permanent memory cost
- **Cons**: 30-60 second startup per scan batch (DB load), complex lifecycle management, risk of leaving clamd running
- **Why not**: Added complexity with marginal benefit; YARA-X covers the primary use cases

### Alternative 3: Use clamscan CLI per file
- **Pros**: No daemon, no permanent memory
- **Cons**: Loads ~300MB virus DB on every invocation (~30s per scan), unusable for real-time
- **Why not**: Too slow for inotify-triggered real-time scanning

## Consequences

### Positive
- Saves ~950MB RAM on every server
- YARA-X is fast (Rust-based), low memory, purpose-built for malware signatures
- Admins who need ClamAV daemon scanning can install it themselves; auto-detected

### Negative
- YARA rules must be maintained and updated (smaller signature set than ClamAV)
- No automatic virus definition updates from ClamAV community (freshclam runs but only for manual clamscan)

### Risks
- YARA rules may miss threats that ClamAV would catch — mitigated by multi-engine approach (heuristic + entropy + YARA-X) and threat intelligence feeds

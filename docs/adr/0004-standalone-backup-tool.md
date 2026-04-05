# ADR-0004: Standalone Backup Tool over Integrated Backup System

**Date**: 2026-04-05
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

The panel had a tightly-coupled backup system: Backup/BackupDestination/BackupSchedule models, BackupOrchestrator service, RunServerBackup/RunAccountBackup jobs, CLI commands, Filament pages for admin and user panels, restore wizard, and download controller. Over 8,000 lines of code across 55+ files.

The system never worked reliably. Issues included: queue worker timeouts killing backup jobs, restic SFTP connection failures with no useful error messages, OPcache not refreshing after code changes, disk space problems on the server, and the fundamental problem of running long backup operations inside a web application's job queue.

A separate `jabali-backup` tool was already built as a standalone CLI with 11 collectors, 9 restorers, per-account snapshots, and multi-backend support. It runs independently of the panel.

## Decision

Remove the entire backup system from the panel (models, jobs, controllers, pages, migrations, translations). Backups are handled by `jabali-backup`, a standalone CLI tool. The panel integrates via the agent addon mechanism — a thin UI layer that calls `jabali-backup` commands through agent RPC routes.

## Alternatives Considered

### Alternative 1: Fix the integrated backup system
- **Pros**: Everything in one codebase, unified UI
- **Cons**: Queue worker unreliable for long operations, tightly coupled, 8000+ lines to maintain
- **Why not**: We tried fixing it (per-account jobs, streaming downloads, error handling) and kept hitting infrastructure issues. The web app is the wrong place for backup orchestration.

### Alternative 2: Keep models in panel, run backups via CLI
- **Pros**: Panel tracks backup state in its own database
- **Cons**: Dual source of truth (panel DB vs restic repo), sync issues, stale data
- **Why not**: Restic is the source of truth for snapshots. Duplicating state in the panel creates drift.

## Consequences

### Positive
- Backups run even when the panel is down
- No dependency on queue worker, database, or OPcache
- 8,000+ lines removed from the panel codebase
- jabali-backup has comprehensive coverage (21 data categories vs panel's 3)
- Standalone tool is testable and debuggable without the web stack
- Agent addon mechanism enables clean integration without patching

### Negative
- Panel UI for backups must be rebuilt as a thin wrapper
- Two repos to maintain (jabali-panel + jabali-backup)
- Snapshot data needs to be queried via CLI rather than database queries

### Risks
- Backup team must follow panel conventions (Filament v5, Tailwind, translations) when building the UI addon. Mitigated by PANEL-INTEGRATION.md documentation.

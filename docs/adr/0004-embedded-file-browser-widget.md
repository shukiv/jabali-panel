# ADR-0004: Embedded file browser widget via adapterClass+adapterConfig

**Date**: 2026-04-06
**Status**: accepted
**Deciders**: shuki, panel team, claude

## Context

The restore modal needs a file browser so admins can select specific files/folders to restore from a restic snapshot. The Jabali panel team maintains a `file-browser-widget` Livewire component that supports pluggable filesystem adapters. We need to embed this widget inside a Filament action modal, passing it a `ResticSnapshotAdapter` that reads file listings from restic snapshots via the agent.

Embedding Livewire components inside Filament modals is non-trivial: the widget needs to survive Livewire re-hydration cycles, receive adapter configuration as serializable data, and communicate selected files back to the parent form.

## Decision

Embed the panel's `file-browser-widget` using Filament's `ViewField` with a blade partial that renders `@livewire('file-browser-widget', [...])`. The adapter is specified via two serializable parameters:

- `adapterClass`: The fully-qualified class name (e.g., `App\Backup\Adapters\ResticSnapshotAdapter::class`)
- `adapterConfig`: An associative array of constructor parameters (e.g., `['snapshot_id' => '...', 'username' => '...']`)

The widget reconstructs the adapter instance internally via `$adapterClass::fromConfig($adapterConfig)`. Selected files are communicated back via Livewire event dispatch, received by the parent page's `onFilesSelected()` listener, and displayed as removable badges using `$wire.entangle('restoreFileList')`.

## Alternatives Considered

### Alternative 1: iframe embedding
- **Pros**: Complete isolation, no integration complexity
- **Cons**: No direct communication with parent form, separate auth context, styling mismatch, clunky UX
- **Why not**: Cannot entangle selected files with the restore form state

### Alternative 2: Rebuild file browser in the addon
- **Pros**: Full control, no dependency on panel widget
- **Cons**: Duplicates existing panel functionality, maintenance burden, divergent UX
- **Why not**: Violates DRY; the panel already has a working, maintained file browser

### Alternative 3: Pass adapter instance directly
- **Pros**: Simpler API (one parameter)
- **Cons**: Livewire cannot serialize arbitrary PHP objects across re-hydration cycles; Laravel DI auto-injects container-bound classes into mount() parameters, overriding the intended instance
- **Why not**: Caused bugs where admin's adapter was injected instead of the snapshot-specific one; `fromConfig()` pattern solves both serialization and DI issues

## Consequences

### Positive
- Reuses the panel team's maintained file browser — consistent UX across the panel
- Adapter pattern is extensible — any filesystem adapter works (local, S3, restic, etc.)
- `adapterClass` + `adapterConfig` survives Livewire re-hydration without serialization issues
- Two-way binding via `$wire.entangle` keeps selected files in sync between Alpine and Livewire

### Negative
- Depends on the panel team's widget API — changes to `file-browser-widget` could break the integration
- The `fromConfig()` / `toArray()` contract must be implemented on every adapter
- Nested Livewire components inside Filament modals required multiple rounds of fixes (trait collisions, DI bugs, selectable missing)

### Risks
- Panel widget API changes — mitigated by the simple `adapterClass`+`adapterConfig` contract which is unlikely to change
- Widget performance with large directories — mitigated by the panel widget's own lazy loading and pagination

# ADR-0005: Temp-file archive for server backup downloads

**Date**: 2026-04-12
**Status**: accepted
**Deciders**: shuki, claude, panel team

## Context

ADR-0001 established named pipes for per-user backup downloads, streaming directly from
restic to the browser with zero temp disk usage. This works well for single-user downloads
where restic starts writing data within milliseconds.

Server backups (disaster recovery) are fundamentally different: the download must combine
the server snapshot with all per-user snapshots into a single archive. Building this
archive requires running multiple `restic restore` operations sequentially (each taking
seconds to minutes) before any data can be streamed.

Using a named pipe for server downloads causes FrankenPHP worker starvation: PHP's
`fopen()` on a FIFO blocks until the writer opens the other end. While restic is still
restoring snapshots (several minutes), the PHP worker is blocked and the entire panel
becomes unresponsive.

## Decision

Server backup downloads use a temp-file approach with a completion sentinel, while
per-user downloads continue using named pipes (ADR-0001).

1. Agent returns `{ archive: "/tmp/...tar.gz", done: "/tmp/...tar.gz.done" }` instead of a pipe path.
2. Background script restores server + all user snapshots, creates the tar.gz, then `touch`es the `.done` sentinel.
3. PHP polls for the `.done` file (up to 15 min) before streaming the completed archive via `readfile()`.
4. Archive + sentinel are cleaned up after streaming.

## Alternatives Considered

### Alternative 1: Named pipe (ADR-0001 approach)
- **Pros**: Zero temp disk usage, consistent with per-user downloads
- **Cons**: `fopen()` blocks PHP worker while restic restores; panel becomes unresponsive
- **Why not**: Multi-snapshot restore takes minutes; FrankenPHP workers are a limited pool

### Alternative 2: Longer PHP timeout on pipe open
- **Pros**: Minimal code change
- **Cons**: Worker is still blocked for minutes; doesn't fix panel unresponsiveness
- **Why not**: Treats the symptom, not the cause

### Alternative 3: Include user data in server snapshot at backup time
- **Pros**: Single snapshot to dump; fast download
- **Cons**: Duplicates data in restic (though dedup mitigates); couples server + user backups; breaks selective per-user restore
- **Why not**: Server backup should be composable from existing per-user snapshots

## Consequences

### Positive
- Panel stays responsive during long server download preparations
- Archive is built once, can be re-downloaded if browser disconnects mid-stream (within TTL)
- Clear separation of concerns: pipes for fast streams, temp files for slow composition

### Negative
- Requires temp disk space equal to the combined archive size (~1x all user data + server configs)
- Download appears "stuck" for the first minute or two while the archive is building
- Cleanup must handle orphaned temp dirs if the background script crashes

### Risks
- Temp dir fills up if cleanup trap fails — mitigated by startup cleanup of stale `/tmp/jabali-server-export-*` dirs
- User waits with no progress indication — future: add a progress endpoint

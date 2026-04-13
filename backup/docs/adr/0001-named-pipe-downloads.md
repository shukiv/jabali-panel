# ADR-0001: Named pipe streaming for downloads

**Date**: 2026-04-06
**Status**: accepted
**Deciders**: shuki, claude

## Context

The panel needs to let admins download backup archives (tar.gz) for one or more users directly from restic snapshots. The CLI already supports `download --output=-` which streams a tar.gz to stdout. The challenge is bridging CLI stdout to the browser without writing large temporary files to disk.

Early attempts used Livewire `streamDownload`, `response()->download()`, and copying to public storage. All failed or produced truncated files due to FrankenPHP buffering, Livewire v3 limitations, and race conditions with large archives.

## Decision

Use named pipes (mkfifo) to connect the CLI process to a dedicated PHP download endpoint.

1. The agent creates a named pipe via `mkfifo /tmp/jb-download-{token}.fifo`.
2. The agent starts the CLI with `download --output=-` writing to the pipe in the background.
3. A standalone PHP endpoint (`public/backup-download.php`) opens the pipe for reading and streams 64 KB chunks to the browser with appropriate `Content-Type: application/gzip` headers.
4. The pipe is cleaned up after the transfer completes or on timeout.

## Alternatives Considered

### Alternative 1: Livewire streamDownload
- **Pros**: Native Livewire, no extra endpoint
- **Cons**: Does not exist in Livewire v3; Livewire response helpers cannot stream large binary data
- **Why not**: API removed in Livewire v3

### Alternative 2: Copy to public storage + redirect
- **Pros**: Simple, works with any web server
- **Cons**: Requires disk space equal to archive size, cleanup complexity, security risk of files in public dir
- **Why not**: Doubles disk usage for large backups, race condition with cleanup

### Alternative 3: Temporary file + token polling
- **Pros**: Works with any PHP setup
- **Cons**: Same disk space issue, polling adds latency, complex token lifecycle
- **Why not**: Over-engineered for the problem, still uses temp files

## Consequences

### Positive
- Zero temporary disk usage — data flows directly from restic to browser
- Works with arbitrarily large archives
- Simple cleanup — pipe is removed after use or on timeout
- Supports multi-user downloads by passing comma-separated usernames

### Negative
- Requires a standalone PHP endpoint outside Filament (cannot use Livewire actions)
- Named pipes are Linux-specific (not portable to Windows, but server is always Linux)
- Pipe blocks if reader or writer dies — timeout/cleanup logic required

### Risks
- FrankenPHP output buffering could truncate streams — mitigated by `ob_end_clean()` and explicit `flush()` calls
- Pipe left dangling if browser disconnects — mitigated by `connection_aborted()` checks and cleanup on shutdown

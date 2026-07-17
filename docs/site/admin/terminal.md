# Terminal

`/jabali-admin/terminal`. In-browser shell on the panel host, scoped to root.

## Use cases

- Quick diagnostic commands when SSH access is awkward (corporate VPN restrictions, mobile device with no SSH client).
- Running a one-off command observed under TLS without exposing an external SSH port.
- Pair-debugging with a colleague over a screen share.

## Auth

The terminal is gated by the admin session cookie. Anyone with a panel admin session can reach it.

## Underlying mechanism

The page opens a WebSocket connection to the panel API, which spawns a PTY under the agent (root). Input and output stream through the WebSocket. The connection is terminated on tab close or 5 minutes of idle inactivity.

## What is logged

- The start and end of each session, plus the source IP, are recorded in [Audit Log](./audit-log.md) (`terminal.session.start` / `terminal.session.end`).
- The keystroke stream is **not** recorded. The audit trail captures who and when, not what was typed.

If keystroke-level recording is required for compliance, run a real SSH session through a session recorder of your choice; the in-panel terminal does not replicate that surface.

## Limitations

- Single-tab. Open two browser tabs for two sessions.
- No tmux / screen multiplexing inside the panel terminal (the terminal element does not interpret all terminal-multiplexer escape sequences correctly). Use `tmux` from a real SSH session.
- No file upload. Use [Files](../user/files.md) (or SFTP) to move files.
- Performance is bounded by the WebSocket latency; for high-throughput operations (large `find`, `tail -f` of a chatty log) use SSH instead.

## Disabling

Operators who do not want this surface enabled can disable it under Server Settings → General → "Enable in-browser terminal" → off. The route returns 404 when disabled.

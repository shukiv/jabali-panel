# Files

`/jabali-panel/files`. The in-panel file manager.

## What you can do

- **Browse** anywhere inside your home directory (`/home/<your-username>`). The manager refuses to navigate above the root; symlinks pointing outside are not followed.
- **Upload** by drag-and-drop. Multiple files at once; chunked transfer for large files. Per-file size is capped by your package's `upload_max_filesize` (the same limit your PHP scripts see).
- **Download** a single file, or select multiple to download as a zip.
- **Create** new files and directories.
- **Rename** files and directories.
- **Delete** files and directories. A confirmation dialog protects against accidental tree removal.
- **Edit** text files in-browser using the Monaco editor (syntax highlighting for HTML, CSS, JS, TS, PHP, Python, Go, Markdown, JSON, YAML, SQL, shell).
- **View** images, archives (list contents), and PDFs.
- **Permissions** — set octal (`755`, `644`) or use per-bit checkboxes.
- **Compress / extract** — tar.gz, zip, 7z.

## What you cannot do

- **Browse outside your home directory.** The manager is chrooted at the page level.
- **Change ownership.** `chown` is a privileged operation; only the agent performs it on your behalf for app installs. You cannot change UIDs.
- **Execute arbitrary shell commands.** This is a file manager, not a terminal. Scripts you upload run only when served by the web server (nginx → PHP-FPM); the manager itself does not shell out.
- **Send files to other users' directories.** Even when permissions on a target directory technically allow it, the path validation rejects writes outside your home.

## How it works

File operations route through the panel API, which calls the agent over Unix socket. The agent enforces the per-user UID constraint at the syscall layer (it never opens files outside `/home/<your-username>`). There is no separate file-browser daemon; the previous `filebrowser` was retired.

## Performance

The page paginates large directory listings (>200 entries per page). Large file uploads (>100 MiB) are chunked and resumable on a dropped connection.

## Hidden files

Hidden files (`.htaccess`, `.git/`, `.env`) are shown by default with a toggle to hide. The toggle is per-session, not persisted.

## When to use SFTP instead

SFTP (see [SSH Keys](./ssh-keys.md)) is more efficient for:

- Bulk transfers (uploading or downloading thousands of files).
- Automation (`rsync`, deploy scripts).
- Large transfers where the browser would time out.

For ad-hoc edits and small transfers, the file manager is faster than firing up an SFTP client.

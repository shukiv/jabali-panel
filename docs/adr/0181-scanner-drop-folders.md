# ADR-0181: Scanner drop folders for shared Files resources

**Status:** Proposed (2026-11-13). Accepted when the branch merges.
**Driven by:** GH #2503 (a network scanner uploads by FTP; a group of mailboxes should see the scans in webmail Files).
**Related:** ADR-0133 (shared resources), ADR-0179 (Stalwart 1.0), GH #1053 / #1171 (FTP/SFTP subaccounts, isolation).

## Context

Office scanners and copiers send scans by FTP or SFTP, not by JMAP or
WebDAV. A Files shared resource (ADR-0133) is a Stalwart 1.0 FileNode tree
owned by a Group principal and shared with mailboxes and groups through
`shareWith`. Nothing an FTP client can reach writes into it.

Stalwart 1.0.2 facts this design rests on (probed on a test host):

- A FileNode create needs `"@type"`: `Directory` or `File`. A File is an
  uploaded blob (`/jmap/upload/<account>/`) plus `FileNode/set create
  {"@type":"File", parentId, name, blobId, mediaType}`.
- A file inherits its folder's `shareWith`. A grant of `{mayRead, mayDelete}`
  lets a member list, download and delete, and refuses create and rename.
- `FileNode/get` with `ids: null` stops at the server's `get_max_objects`
  without saying so; a full listing pages `FileNode/query` by `parentId`
  and gets the ids in chunks.
- The Group that owns a shared resource has no quota, so what lands in the
  folder is limited only by what the panel allows.

## Decision

1. **An FTP drop folder, moved by the panel.** Turning a scanner on makes
   `<home>/domains/<domain>/scans/<resource>` (outside `public_html`) and an
   FTP/SFTP subaccount homed there, through `ftpops.Create`, so it counts
   against the package's FTP account cap like any other login. A row in
   `scan_drops` ties the login to the resource. Every 30 seconds the panel
   asks the agent (`scandrop.ingest`) to move finished files into the folder.
   The scan is moved, not copied: the drop folder is a queue, not storage.
2. **The login is kept to its folder, or refused.** Where disk quota exists
   the login is isolated (own uid, bind-mounted jail, quota = the space
   setting). Without quota a subaccount is a same-uid alias, and its SFTP
   session chroots to the tenant's whole home (the sshd chroot must be
   root-owned, so it cannot be the drop folder). Such a login gets FTPS only,
   where vsftpd chroots it to the drop folder, and turning the scanner on is
   refused (`scanner_unconfined`) when the FTP server is off.
3. **Finished means settled and unlocked.** A file moves once its size and
   times have not changed for 60 seconds and vsftpd holds no write lock on it.
   Dot files and partial names wait. Only regular files owned by the tenant
   or the scanner login are read, opened `O_NOFOLLOW` under an `openat2`
   `RESOLVE_BENEATH` root, so a symlink or a planted hard link to another
   file cannot be filed into the shared folder.
4. **Remove only what was filed.** The agent uploads the blob, re-checks the
   open file did not change while uploading, creates the FileNode, and then
   unlinks the drop file only if it is still the same file (device, inode,
   size, mtime). A file already present under its name, or under a numbered
   copy of it, with the same blob is not filed twice after a crash between
   create and unlink. A name clash gets ` (n)`.
5. **The space setting caps the folder.** The pass stops moving when the
   folder's total would pass the space setting and reports why; members make
   room by deleting. A file larger than Stalwart's `maxSizeUpload` stays and
   says so.
6. **Read + delete is a grant level for Files only.** `readdelete` maps to
   `{mayRead, mayDelete}`. It is refused on other kinds, at the API and the
   CLI.
7. **Cleanup is a sweep, not a hook.** `scan_drops` has no foreign keys. The
   ingest loop removes the login and the row when the resource is gone, and
   the row when the login is gone. Turning the scanner off removes the login
   and leaves files in both places.

## Consequences

- A domain rename is refused while the scanner's login lives under the
  domain's directory (the existing FTP-home rename gate). The owner turns
  the scanner off first.
- Scans in the shared folder do not count against the tenant's disk quota;
  the space setting is the only limit there.
- The scanner can be any FTPS or SFTP client. Plain FTP stays off unless the
  administrator allows cleartext for the whole FTP server.
- A scan is in the shared folder 60 to 90 seconds after its upload finishes.

## Alternatives considered

- **Copy and keep the drop file.** Doubles storage and leaves a second copy
  the members cannot delete.
- **WebDAV straight into Stalwart.** Few scanners speak WebDAV, and those
  that do would need a mailbox password.
- **A folder under `public_html`.** Scans would be one wrong rule away from
  the web.
- **inotify in the agent.** Needs a long-lived watcher per folder and still
  has to decide when a file is finished; a 30-second sweep does both and
  survives restarts.

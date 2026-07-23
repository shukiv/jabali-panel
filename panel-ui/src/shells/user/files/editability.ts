import type { FileEntry } from "./filesApi";

// EDIT_MAX_BYTES matches the server preview cap (files.read default 1 MiB).
// Gating Edit at this size means the editor always loads the FULL file, so a
// save can never truncate a larger file on disk.
export const EDIT_MAX_BYTES = 1024 * 1024;

// isTextEditable decides whether to OFFER the per-row "Edit" item. It allows
// ANY file small enough to load fully — dotfiles (.bashrc, .htaccess) and
// extensionless configs included, not just whitelisted extensions. openEditor
// still refuses real binaries (mime sniff + NUL bytes), so an accidental Edit
// on a binary fails gracefully rather than corrupting it.
//
// Empty (0-byte) files ARE editable: creating a file then editing it is the
// whole point (GH #532 — a freshly-created test.txt showed no Edit item
// because the old gate required size > 0). A 0-byte file loads as "" and
// saves fine. Only the upper bound matters.
export function isTextEditable(entry: FileEntry): boolean {
  if (entry.is_dir || entry.is_symlink) return false;
  return entry.size >= 0 && entry.size < EDIT_MAX_BYTES;
}

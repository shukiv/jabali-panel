// FileManagerPage — AntD-native file manager for /jabali-panel/files (M11).
//
// Layout:
//   Breadcrumb + action buttons (top)
//   Tree (left, lazy-loaded dirs)  |  Table (right, entries of current dir)
//
// Primary ops per row: download, preview (text, ≤1 MiB), rename, delete.
// Toolbar: upload file, new folder, refresh.
//
// Drag-and-drop (added post-Phase-1):
//   - Drop OS files on the table to upload to currentPath (AntD Dragger).
//   - Drag a row onto a folder row (or onto a tree node) to move the file
//     there. Cross-directory move goes through /files/move, which is
//     distinct from rename (same-parent only).
//
// Scope: still no multi-select, no chmod, no image preview, no editor —
// those remain Phase 2.
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import {
  Breadcrumb,
  Button,
  Card,
  Drawer,
  Dropdown,
  Empty,
  Grid,
  Input,
  Modal,
  Space,
  Spin,
  Table,
  Tag,
  Tree,
  Typography,
  message,
  theme,
} from "antd";
import type { DataNode } from "antd/es/tree";
import {
  DeleteOutlined,
  DownOutlined,
  FileAddOutlined,
  DownloadOutlined,
  EditOutlined,
  EyeOutlined,
  CloseOutlined,
  CopyOutlined,
  FileArchiveOutlined,
  FileCodeOutlined,
  FileImageOutlined,
  FileMinusOutlined,
  FileOutlined,
  FilePlayOutlined,
  FileTextOutlined,
  FileVolumeOutlined,
  FolderInputOutlined,
  FolderAddOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  LockOutlined,
  MoreOutlined,
  PlusOutlined,
  ReloadOutlined,
  UploadOutlined,
} from "@icons";
import { AxiosError } from "axios";

import type { FileEntry } from "./filesApi";
import {
  filesArchive,
  filesChmod,
  filesCopy,
  filesDelete,
  filesExtract,
  filesDownloadURL,
  filesHome,
  filesList,
  filesMkdir,
  filesMove,
  filesPreview,
  filesRename,
  filesTree,
  filesWrite,
} from "./filesApi";
import { UploadDrawer } from "./UploadDrawer";
import type { UploadDrawerHandle } from "./UploadDrawer";
// Monaco is ~500KB gzipped of the bundle; lazy-load it so the initial
// files page doesn't pay that cost. The editor only mounts when the
// user actually clicks Edit on a file. Suspense fallback below keeps
// the modal from flashing an empty pane while the chunk is fetched.
//
// We MUST self-host monaco — the default @monaco-editor/react loader
// fetches the AMD bundle from cdn.jsdelivr.net which is blocked by
// the panel's strict CSP (script-src 'self' 'unsafe-inline'). Pass
// the bundled monaco-editor module to loader.config so no network
// fetch happens; the chunk lives inside our own vite-built bundle.
const Editor = lazy(async () => {
  const [{ loader }, monacoMod, EditorWorker] = await Promise.all([
    import("@monaco-editor/react"),
    // JAB-146: slim Monaco (editor core + basic-language grammars only) —
    // drops the multi-MB ts.worker / css / html / json language services.
    import("./monacoSlim"),
    import("monaco-editor/esm/vs/editor/editor.worker?worker"),
  ]);
  const monaco = monacoMod.default;
  // Monaco resolves language workers via self.MonacoEnvironment. We
  // only need the base editor worker for plain-text editing of
  // wp-config.php / .htaccess / etc.; JSON / CSS / HTML / TS
  // language workers are deliberately not wired so the bundle stays
  // small (~500KB instead of ~3MB).
  (self as unknown as { MonacoEnvironment: { getWorker: () => Worker } }).MonacoEnvironment = {
    getWorker: () => new EditorWorker.default(),
  };
  loader.config({ monaco });
  return import("@monaco-editor/react");
});

const { Text } = Typography;

// Custom DataTransfer MIME for row drags, so the parent OS-file drop
// handler can distinguish a "dragging a row around inside the table"
// event from a "dragging a file in from Finder" event. The payload is
// always a JSON-encoded string[] of absolute paths — single-row drags
// are a one-element array, multi-select drags send all selected rows.
const dragPathMime = "application/x-jabali-file-path";

// parseDragPayload accepts both the new array-JSON form and the legacy
// single-path form (pre-bulk-selection) so a row in flight at the
// moment of deploy doesn't land as a silent no-op.
function parseDragPayload(raw: string): string[] {
  try {
    const v = JSON.parse(raw);
    if (Array.isArray(v) && v.every((x) => typeof x === "string")) {
      return v;
    }
  } catch {
    // not JSON — fall through to treat as single path
  }
  return raw ? [raw] : [];
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let size = bytes;
  let i = 0;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return i === 0 ? `${Math.floor(size)} B` : `${size.toFixed(1)} ${units[i]}`;
}

function formatModTime(raw: string): string {
  // Agent emits Go's default time.String() format, e.g.
  //   "2026-04-18 19:41:23.123456789 +0000 UTC"
  // Trim everything after seconds for display.
  return raw.slice(0, 19);
}

function joinPath(dir: string, name: string): string {
  return dir.endsWith("/") ? dir + name : dir + "/" + name;
}

// isImagePath returns true for extensions the browser can render inline
// via an <img> tag. Used to switch the preview modal from the text
// view to an image view without forcing an intermediate API call.
const imageExtensions = new Set([
  ".png",
  ".jpg",
  ".jpeg",
  ".gif",
  ".webp",
  ".svg",
  ".bmp",
  ".ico",
  ".avif",
]);

function isImagePath(name: string): boolean {
  const i = name.lastIndexOf(".");
  if (i < 0) return false;
  return imageExtensions.has(name.slice(i).toLowerCase());
}

// EDIT_MAX_BYTES matches the server preview cap (files.read default 1 MiB).
// Gating Edit at this size means the editor always loads the FULL file, so a
// save can never truncate a larger file on disk.
const EDIT_MAX_BYTES = 1024 * 1024;

// isTextEditable decides whether to OFFER the per-row "Edit" item. It allows
// ANY file small enough to load fully — dotfiles (.bashrc, .htaccess) and
// extensionless configs included, not just whitelisted extensions. openEditor
// still refuses real binaries (mime sniff + NUL bytes), so an accidental Edit
// on a binary fails gracefully rather than corrupting it.
function isTextEditable(entry: FileEntry): boolean {
  if (entry.is_dir || entry.is_symlink) return false;
  return entry.size > 0 && entry.size < EDIT_MAX_BYTES;
}

// Monaco language key by extension. Only common ones are mapped; other
// files still open in plain text mode.
const languageByExt: Record<string, string> = {
  ".js": "javascript",
  ".jsx": "javascript",
  ".mjs": "javascript",
  ".cjs": "javascript",
  ".ts": "typescript",
  ".tsx": "typescript",
  ".json": "json",
  ".html": "html",
  ".htm": "html",
  ".css": "css",
  ".scss": "scss",
  ".less": "less",
  ".md": "markdown",
  ".markdown": "markdown",
  ".php": "php",
  ".py": "python",
  ".rb": "ruby",
  ".go": "go",
  ".rs": "rust",
  ".java": "java",
  ".kt": "kotlin",
  ".sh": "shell",
  ".bash": "shell",
  ".zsh": "shell",
  ".fish": "shell",
  ".sql": "sql",
  ".xml": "xml",
  ".yaml": "yaml",
  ".yml": "yaml",
  ".toml": "ini",
  ".ini": "ini",
  ".env": "ini",
  ".conf": "ini",
};

function languageFor(name: string): string {
  const i = name.lastIndexOf(".");
  if (i < 0) return "plaintext";
  return languageByExt[name.slice(i).toLowerCase()] || "plaintext";
}

// symbolicModeToOctal converts Go's Mode().String() (e.g. "-rw-r--r--",
// "drwxr-xr-x") back to a 3-digit octal ("644", "755"). Returns "" if
// the input isn't recognisable so the caller can fall back to a default.
// setuid/setgid/sticky ("s"/"S"/"t"/"T") are collapsed into their
// x-only equivalents for display; the chmod editor + text input still
// accept the full 4-digit form if the user needs to write those bits.
function symbolicModeToOctal(s: string): string {
  if (s.length < 10) return "";
  const rwx = s.slice(s.length - 9); // last 9 chars
  let out = "";
  for (let g = 0; g < 3; g++) {
    const seg = rwx.slice(g * 3, g * 3 + 3);
    let d = 0;
    if (seg[0] === "r") d |= 4;
    if (seg[1] === "w") d |= 2;
    // x, s (setuid/setgid with x), t (sticky with x) all count as exec
    if (seg[2] === "x" || seg[2] === "s" || seg[2] === "t") d |= 1;
    out += d.toString();
  }
  return out;
}

// Folders-first, then alphabetical (case-insensitive, locale-aware).
// Matches every desktop file manager's default — GNOME Files, Finder, Explorer —
// and keeps dotfiles/dotdirs naturally sorted within their group.
function sortEntries(entries: FileEntry[]): FileEntry[] {
  return [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
}

function errMessage(err: unknown): string {
  if (err instanceof AxiosError) {
    const data = err.response?.data as { detail?: string; error?: string } | undefined;
    // 507 Insufficient Storage: agent returned EDQUOT (user over disk
    // quota) or ENOSPC (filesystem full). Either way the user-facing
    // message is the same thing: your disk is full — free space or ask
    // admin for more quota. Don't leak the internal syscall text.
    if (err.response?.status === 507 || data?.error === "quota_exceeded") {
      return "Disk quota exceeded — free up space or ask your admin for more.";
    }
    if (data?.error === "disk_full") {
      return "Server disk is full. Contact your admin.";
    }
    return data?.detail || data?.error || err.message;
  }
  if (err instanceof Error) return err.message;
  return "Unexpected error";
}

type TreeNode = DataNode & { path: string };

function makeTreeNode(path: string, name: string): TreeNode {
  return {
    key: path,
    title: name,
    path,
    isLeaf: false,
    icon: <FolderAddOutlined style={{ color: "#faad14", fontSize: 18 }} />,
  };
}

export const FileManagerPage = () => {
  const { t } = useTranslation();
  // Pull live theme tokens so the bulk-action bar (and any other
  // tinted surface here) tracks the global light/dark palette — we had
  // hard-coded #f0f5ff / #adc6ff / dim text before and those only
  // read correctly on the light theme.
  const { token } = theme.useToken();
  const screens = Grid.useBreakpoint();
  // Tree pane sits inline on tablet+ (md ≥768). Below md it moves
  // into a left Drawer opened by a Folders button — no room for a
  // 280px pane plus a file list on phones.
  const inlineTree = screens.md !== false;
  const [treeDrawerOpen, setTreeDrawerOpen] = useState(false);
  const [uploadDrawerOpen, setUploadDrawerOpen] = useState(false);
  const uploadDrawerRef = useRef<UploadDrawerHandle | null>(null);

  const [rootPath, setRootPath] = useState<string | null>(null);
  const [currentPath, setCurrentPath] = useState<string | null>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  // Directory is reflected in the URL (?path=) so browser back/forward and
  // deep-links work (GH #315). navigateToPath pushes a history entry; the
  // effect below flows URL changes (incl. back/forward) into currentPath,
  // which the list-reload effect already keys on.
  const navigateToPath = useCallback(
    (p: string) => {
      setSearchParams((prev) => {
        const next = new URLSearchParams(prev);
        next.set("path", p);
        return next;
      });
    },
    [setSearchParams],
  );
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [listLoading, setListLoading] = useState(false);
  const [treeData, setTreeData] = useState<TreeNode[]>([]);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);

  const [mkdirOpen, setMkdirOpen] = useState(false);
  const [mkdirName, setMkdirName] = useState("");
  const mkdirSubmitting = useRef(false);
  const [newFileOpen, setNewFileOpen] = useState(false);
  const [newFileName, setNewFileName] = useState("");
  const newFileSubmitting = useRef(false);

  const [renameOpen, setRenameOpen] = useState(false);
  const [renameTarget, setRenameTarget] = useState<FileEntry | null>(null);
  const [renameNewName, setRenameNewName] = useState("");
  const renameSubmitting = useRef(false);

  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; entry: FileEntry } | null>(null);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  const [previewContent, setPreviewContent] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);

  // --- bulk selection ---
  // selectedNames holds the row keys (entry.name) of checked rows in the
  // CURRENT dir. Cleared whenever currentPath changes — mixing selections
  // across directories would be surprising and makes the "Delete N" button
  // lie about what it'll actually delete.
  const [selectedNames, setSelectedNames] = useState<string[]>([]);

  const [bulkMoveOpen, setBulkMoveOpen] = useState(false);
  const [bulkMoveDest, setBulkMoveDest] = useState("");
  const [bulkChmodOpen, setBulkChmodOpen] = useState(false);
  const [bulkChmodMode, setBulkChmodMode] = useState("0644");

  // Single-entry chmod. Separate from the bulk path so the per-row
  // "Permissions" menu item doesn't require checking the row first.
  // Mode seeded from the row's parsed octal when opening.
  const [chmodTarget, setChmodTarget] = useState<FileEntry | null>(null);
  const [chmodTargetMode, setChmodTargetMode] = useState("0644");

  // Clipboard for Copy/Paste. Stores the absolute paths captured at
  // Copy time — Paste resolves them against the current folder. Cut
  // semantics (move-on-paste) is out of scope for v1 since the Copy
  // button is enough for the most common case, and move is already
  // one drag away.
  const [clipboard, setClipboard] = useState<string[]>([]);

  // Editor — Monaco-based. editTarget holds the path of the file
  // being edited; null means the modal is closed.
  const [editTarget, setEditTarget] = useState<string | null>(null);
  const [editOriginal, setEditOriginal] = useState("");
  const [editContent, setEditContent] = useState("");
  const [editLoading, setEditLoading] = useState(false);
  const [editSaving, setEditSaving] = useState(false);

  // --- initial load: fetch user's home then list it ---
  useEffect(() => {
    (async () => {
      try {
        const home = await filesHome();
        setRootPath(home.path);
        setTreeData([makeTreeNode(home.path, home.path)]);
        setExpandedKeys([home.path]);
        // Deep-link: honor ?path= on first load, else open home (replace so
        // the initial entry isn't a dead back target).
        const urlPath = searchParams.get("path");
        if (urlPath) {
          setCurrentPath(urlPath);
        } else {
          setCurrentPath(home.path);
          setSearchParams(
            (prev) => {
              const next = new URLSearchParams(prev);
              next.set("path", home.path);
              return next;
            },
            { replace: true },
          );
        }
      } catch (err) {
        message.error(`Cannot open file manager: ${errMessage(err)}`);
      }
    })();
  }, []);

  // Flow URL changes (back/forward, deep-link) into currentPath. Guarded so
  // our own navigateToPath set doesn't loop (GH #315).
  useEffect(() => {
    const p = searchParams.get("path");
    if (p && p !== currentPath) setCurrentPath(p);
  }, [searchParams, currentPath]);

  // --- list current dir ---
  const reloadList = useCallback(async (path: string) => {
    setListLoading(true);
    try {
      const resp = await filesList(path);
      setEntries(sortEntries(resp.entries));
    } catch (err) {
      message.error(`List failed: ${errMessage(err)}`);
      setEntries([]);
    } finally {
      setListLoading(false);
    }
  }, []);

  useEffect(() => {
    if (currentPath) void reloadList(currentPath);
    // Moving between folders wipes the selection — holding it would mean
    // a "Delete 3 items" button that referenced rows you could no longer see.
    setSelectedNames([]);
  }, [currentPath, reloadList]);

  // Esc clears the selection — matches every desktop file manager.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setSelectedNames([]);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // --- tree: lazy-load children on expand ---
  const loadTreeChildren = useCallback(async (node: TreeNode): Promise<void> => {
    try {
      const resp = await filesTree(node.path);
      // Backend sets has_subdirs per entry; fold it into isLeaf so the
      // tree hides the chevron on leaf folders — no "expand to discover
      // it's empty" round trip.
      const children: TreeNode[] = resp.entries.map((e) => ({
        key: joinPath(node.path, e.name),
        title: e.name,
        path: joinPath(node.path, e.name),
        isLeaf: !e.has_subdirs,
        icon: <FolderAddOutlined style={{ color: '#faad14', fontSize: 18 }} />,
      }));
      setTreeData((prev) => updateTreeNode(prev, node.path, children));
    } catch (err) {
      message.error(`Tree load failed: ${errMessage(err)}`);
    }
  }, []);

  // --- actions ---
  const handleRefresh = () => {
    if (currentPath) void reloadList(currentPath);
    // Also refresh the expanded tree nodes up to current so new dirs show.
    if (rootPath) void loadTreeChildren({ key: rootPath, title: rootPath, path: rootPath });
  };

  const handleRowClick = (entry: FileEntry) => {
    if (!currentPath) return;
    if (entry.is_dir) {
      const next = joinPath(currentPath, entry.name);
      navigateToPath(next);
      // Expand the parent (currentPath) and lazy-load its children so the
      // new child appears in the tree on the left. Without this, drilling
      // down via the table leaves the tree stuck at the last node the user
      // expanded manually via the chevron.
      setExpandedKeys((prev) => {
        const s = new Set(prev);
        s.add(currentPath);
        s.add(next);
        return Array.from(s);
      });
      void loadTreeChildren({ key: currentPath, title: currentPath, path: currentPath });
    }
  };

  const handlePreview = async (entry: FileEntry) => {
    if (!currentPath) return;
    const path = joinPath(currentPath, entry.name);
    setPreviewPath(path);
    setPreviewContent("");
    setPreviewOpen(true);
    // Image branch: skip the text-preview round-trip, the browser fetches
    // the bytes directly via the download URL and renders them inline.
    if (isImagePath(entry.name)) {
      setPreviewLoading(false);
      return;
    }
    setPreviewLoading(true);
    try {
      const resp = await filesPreview(path);
      setPreviewContent(resp.content);
    } catch (err) {
      message.error(`Preview failed: ${errMessage(err)}`);
      setPreviewOpen(false);
    } finally {
      setPreviewLoading(false);
    }
  };

  const handleDownload = (entry: FileEntry) => {
    if (!currentPath) return;
    const path = joinPath(currentPath, entry.name);
    // Open in a new tab; the browser will save as an attachment due to
    // our server-side Content-Disposition header.
    window.open(filesDownloadURL(path), "_blank", "noopener,noreferrer");
  };

  const handleDelete = async (entry: FileEntry) => {
    if (!currentPath) return;
    const path = joinPath(currentPath, entry.name);
    try {
      await filesDelete(path, entry.is_dir);
      message.success(`Deleted ${entry.name}`);
      void reloadList(currentPath);
    } catch (err) {
      message.error(`Delete failed: ${errMessage(err)}`);
    }
  };

  const confirmDelete = (entry: FileEntry) => {
    Modal.confirm({
      title: `Delete "${entry.name}"?`,
      content: entry.is_dir ? "Folder and everything inside will be removed." : undefined,
      okText: "Delete",
      okButtonProps: { danger: true },
      onOk: () => handleDelete(entry),
    });
  };

  const openRename = (entry: FileEntry) => {
    setRenameTarget(entry);
    setRenameNewName(entry.name);
    setRenameOpen(true);
  };

  // Seed the single-entry chmod modal from the row's actual mode when
  // we can parse it; fall back to 0755 for dirs / 0644 for files.
  const openSingleChmod = (entry: FileEntry) => {
    const oct = symbolicModeToOctal(entry.mode);
    setChmodTargetMode(oct ? `0${oct}` : entry.is_dir ? "0755" : "0644");
    setChmodTarget(entry);
  };

  const openEditor = useCallback(
    async (entry: FileEntry) => {
      if (!currentPath) return;
      const path = joinPath(currentPath, entry.name);
      setEditTarget(path);
      setEditLoading(true);
      setEditContent("");
      setEditOriginal("");
      try {
        const resp = await filesPreview(path);
        // Refuse to open binaries. Two signals, either one is enough:
        //   1. Server-sniffed mime_type says non-text (image, octet-stream, ...)
        //   2. Content contains NUL bytes (classic binary smell; text never has
        //      them, and JSON preserves \u0000 literally so we actually see it)
        const mime = (resp.mime_type || "").toLowerCase();
        const mimeIsText =
          mime.startsWith("text/") ||
          mime.startsWith("application/json") ||
          mime.startsWith("application/xml") ||
          mime.startsWith("application/x-sh") ||
          mime.startsWith("application/javascript") ||
          mime === "";
        const hasNul = resp.content.includes("\u0000");
        if (!mimeIsText || hasNul) {
          message.error("Cannot edit: file looks binary");
          setEditTarget(null);
          return;
        }
        setEditContent(resp.content);
        setEditOriginal(resp.content);
      } catch (err) {
        message.error(`Load failed: ${errMessage(err)}`);
        setEditTarget(null);
      } finally {
        setEditLoading(false);
      }
    },
    [currentPath],
  );

  const submitEdit = useCallback(async () => {
    if (!editTarget || editSaving) return;
    // Guard: the editor is text-only. If somehow a NUL slipped in (paste
    // from a hex dump? browser extension?) refuse rather than silently
    // corrupting what's on disk.
    if (editContent.includes("\u0000")) {
      message.error("Refusing to save: content contains NUL bytes");
      return;
    }
    setEditSaving(true);
    try {
      await filesWrite(editTarget, editContent);
      message.success("Saved");
      setEditTarget(null);
      if (currentPath) void reloadList(currentPath);
    } catch (err) {
      message.error(`Save failed: ${errMessage(err)}`);
    } finally {
      setEditSaving(false);
    }
  }, [editTarget, editContent, editSaving, currentPath, reloadList]);

  const submitSingleChmod = async () => {
    if (!chmodTarget || !currentPath) return;
    const mode = chmodTargetMode.trim();
    if (!/^[0-7]{3,4}$/.test(mode)) {
      message.error("Mode must be an octal string like 644 or 0755");
      return;
    }
    const path = joinPath(currentPath, chmodTarget.name);
    try {
      await filesChmod(path, mode);
      message.success(`Permissions updated`);
      setChmodTarget(null);
      void reloadList(currentPath);
    } catch (err) {
      message.error(`Chmod failed: ${errMessage(err)}`);
    }
  };

  const submitRename = async () => {
    if (!currentPath || !renameTarget || renameSubmitting.current) return;
    const newName = renameNewName.trim();
    if (!newName || newName.includes("/") || newName === "." || newName === "..") {
      message.error("Invalid new name");
      return;
    }
    renameSubmitting.current = true;
    try {
      const path = joinPath(currentPath, renameTarget.name);
      await filesRename(path, newName);
      message.success(`Renamed to ${newName}`);
      setRenameOpen(false);
      void reloadList(currentPath);
    } catch (err) {
      message.error(`Rename failed: ${errMessage(err)}`);
    } finally {
      renameSubmitting.current = false;
    }
  };

  const openMkdir = () => {
    setMkdirName("");
    setMkdirOpen(true);
  };

  const submitMkdir = async () => {
    if (!currentPath || mkdirSubmitting.current) return;
    const name = mkdirName.trim();
    if (!name || name.includes("/") || name === "." || name === "..") {
      message.error("Invalid folder name");
      return;
    }
    mkdirSubmitting.current = true;
    try {
      await filesMkdir(joinPath(currentPath, name));
      message.success(`Created ${name}`);
      setMkdirOpen(false);
      void reloadList(currentPath);
    } catch (err) {
      message.error(`Create folder failed: ${errMessage(err)}`);
    } finally {
      mkdirSubmitting.current = false;
    }
  };

  const openNewFile = () => {
    setNewFileName("");
    setNewFileOpen(true);
  };

  const submitNewFile = async () => {
    if (!currentPath || newFileSubmitting.current) return;
    const name = newFileName.trim();
    if (!name || name.includes("/") || name === "." || name === "..") {
      message.error("Invalid file name");
      return;
    }
    // Guard: never overwrite an existing entry with an empty file.
    if (entries.some((e) => e.name === name)) {
      message.error(`"${name}" already exists`);
      return;
    }
    newFileSubmitting.current = true;
    try {
      await filesWrite(joinPath(currentPath, name), "");
      message.success(`Created ${name}`);
      setNewFileOpen(false);
      void reloadList(currentPath);
    } catch (err) {
      message.error(`Create file failed: ${errMessage(err)}`);
    } finally {
      newFileSubmitting.current = false;
    }
  };

  // Upload entrypoint. Click "Upload" or drag OS files onto the table
  // → enqueue into the UploadDrawer, which owns concurrency, per-file
  // progress bars, and error reporting. The drawer auto-opens on the
  // first enqueue.
  const enqueueUpload = useCallback((file: File) => {
    uploadDrawerRef.current?.enqueue(file);
  }, []);

  // --- drag-to-move state ---
  // draggedPath is set on dragstart from a table row; consumed on drop
  // onto a folder row (or tree node) and cleared on dragend.
  const [draggedPath, setDraggedPath] = useState<string | null>(null);

  // --- bulk ops ---
  // Each "bulk X" handler fans out into parallel API calls via
  // Promise.allSettled so a single failure doesn't cancel the rest.
  // The toast summarises N succeeded / M failed; detailed per-item errors
  // still land in the browser console for now. Good enough for v1.

  const selectedPaths = useMemo(() => {
    if (!currentPath) return [] as string[];
    return selectedNames.map((n) => joinPath(currentPath, n));
  }, [currentPath, selectedNames]);

  const selectedEntries = useMemo(
    () => entries.filter((e) => selectedNames.includes(e.name)),
    [entries, selectedNames],
  );

  const runBulk = useCallback(
    async (
      verb: string,
      paths: string[],
      op: (p: string) => Promise<void>,
    ) => {
      const results = await Promise.allSettled(paths.map(op));
      const ok = results.filter((r) => r.status === "fulfilled").length;
      const fail = results.length - ok;
      if (fail === 0) {
        message.success(`${verb} ${ok} item${ok === 1 ? "" : "s"}`);
      } else if (ok === 0) {
        message.error(`${verb} failed for all ${fail} items`);
      } else {
        message.warning(`${verb} ${ok}/${results.length} — ${fail} failed`);
      }
      // Log failures for debug — the toast can't surface per-item detail.
      results.forEach((r, i) => {
        if (r.status === "rejected") {
          console.warn(`[files bulk] ${verb} ${paths[i]} failed:`, r.reason);
        }
      });
      setSelectedNames([]);
      if (currentPath) void reloadList(currentPath);
    },
    [currentPath, reloadList],
  );

  const handleBulkDelete = useCallback(() => {
    if (selectedPaths.length === 0) return;
    const anyDir = selectedEntries.some((e) => e.is_dir);
    Modal.confirm({
      title: `Delete ${selectedPaths.length} item${selectedPaths.length === 1 ? "" : "s"}?`,
      content: anyDir
        ? "Folders will be removed with everything inside them. This cannot be undone."
        : "This cannot be undone.",
      okType: "danger",
      okText: "Delete",
      onOk: () =>
        runBulk("Deleted", selectedPaths, (p) => {
          const entry = selectedEntries.find(
            (e) => joinPath(currentPath || "", e.name) === p,
          );
          return filesDelete(p, entry?.is_dir ?? false);
        }),
    });
  }, [currentPath, runBulk, selectedEntries, selectedPaths]);

  const handleBulkMove = useCallback(async () => {
    const dest = bulkMoveDest.trim();
    if (!dest || !dest.startsWith("/")) {
      message.error("Destination must be an absolute path starting with /");
      return;
    }
    setBulkMoveOpen(false);
    await runBulk("Moved", selectedPaths, (p) => filesMove(p, dest));
    setBulkMoveDest("");
  }, [bulkMoveDest, runBulk, selectedPaths]);

  const handleBulkChmod = useCallback(async () => {
    const mode = bulkChmodMode.trim();
    if (!/^[0-7]{3,4}$/.test(mode)) {
      message.error("Mode must be an octal string like 644 or 0755");
      return;
    }
    setBulkChmodOpen(false);
    await runBulk("Chmod", selectedPaths, (p) => filesChmod(p, mode));
  }, [bulkChmodMode, runBulk, selectedPaths]);

  // Download selection as archive.tar.gz. Single HTTP request, the
  // backend builds the tarball server-side and streams back — no
  // in-memory concatenation on the client, so very large selections
  // work as long as the backend cap (500 MB total) is respected.
  const handleBulkDownloadZip = useCallback(async () => {
    if (selectedPaths.length === 0) return;
    try {
      const blob = await filesArchive(selectedPaths);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "archive.tar.gz";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      message.success(`Archived ${selectedPaths.length} items`);
    } catch (err) {
      message.error(`Archive failed: ${errMessage(err)}`);
    }
  }, [selectedPaths]);

  const handleCopyToClipboard = useCallback(() => {
    if (selectedPaths.length === 0) return;
    setClipboard(selectedPaths);
    message.success(
      `Copied ${selectedPaths.length} item${selectedPaths.length === 1 ? "" : "s"} — switch folders and Paste`,
    );
    setSelectedNames([]);
  }, [selectedPaths]);

  const handlePaste = useCallback(async () => {
    if (!currentPath || clipboard.length === 0) return;
    await runBulk("Pasted", clipboard, (p) => filesCopy(p, currentPath));
    setClipboard([]);
  }, [clipboard, currentPath, runBulk]);

  const handleMove = useCallback(
    async (srcPath: string, destDir: string) => {
      // Refuse a no-op upfront — destDir === dirname(srcPath) means the
      // user dropped onto the same folder the row already lives in. The
      // backend would refuse with "source and destination are the same",
      // but failing silently here keeps the UI calm.
      const lastSlash = srcPath.lastIndexOf("/");
      const srcParent = lastSlash > 0 ? srcPath.slice(0, lastSlash) : "/";
      if (srcParent === destDir) return;
      try {
        await filesMove(srcPath, destDir);
        message.success("Moved");
        if (currentPath) void reloadList(currentPath);
      } catch (err) {
        message.error(`Move failed: ${errMessage(err)}`);
      }
    },
    [currentPath, reloadList],
  );

  // --- breadcrumb segments ---
  const crumbs = useMemo(() => {
    if (!currentPath || !rootPath) return [];
    // Clamp displayed breadcrumbs to rootPath so user can't navigate above home.
    const rel = currentPath.startsWith(rootPath) ? currentPath.slice(rootPath.length) : "";
    const parts = rel.split("/").filter(Boolean);
    const items: { title: ReactNode; path: string }[] = [
      { title: <FolderOutlined />, path: rootPath },
    ];
    let acc = rootPath;
    for (const part of parts) {
      acc = joinPath(acc, part);
      items.push({ title: part, path: acc });
    }
    return items;
  }, [currentPath, rootPath]);

  const EXTRACTABLE_EXTS = [
    ".zip",
    ".tar",
    ".tar.gz",
    ".tgz",
    ".tar.bz2",
    ".tbz2",
    ".gz",
  ];
  const isArchive = (entry: FileEntry) => {
    if (entry.is_dir) return false;
    const n = entry.name.toLowerCase();
    return EXTRACTABLE_EXTS.some((e) => n.endsWith(e));
  };

  const handleExtract = async (entry: FileEntry) => {
    if (!currentPath) return;
    const path = joinPath(currentPath, entry.name);
    try {
      const res = await filesExtract(path);
      const skipped =
        res.skipped > 0 ? `, ${res.skipped} unsafe entr(y/ies) skipped` : "";
      message.success(`Extracted ${res.extracted} file(s)${skipped}`);
      void reloadList(currentPath);
    } catch (err) {
      message.error(`Extract failed: ${errMessage(err)}`);
    }
  };

  const buildRowMenuItems = (entry: FileEntry) => [
    ...(!entry.is_dir
      ? [
          {
            key: "download",
            icon: <DownloadOutlined />,
            label: "Download",
            onClick: () => handleDownload(entry),
          },
          {
            key: "preview",
            icon: <EyeOutlined />,
            label: "Preview",
            onClick: () => void handlePreview(entry),
          },
        ]
      : []),
    ...(isArchive(entry)
      ? [
          {
            key: "extract",
            icon: <FolderOpenOutlined />,
            label: "Extract here",
            onClick: () => void handleExtract(entry),
          },
        ]
      : []),
    ...(isTextEditable(entry)
      ? [
          {
            key: "edit",
            icon: <EditOutlined />,
            label: "Edit",
            onClick: () => void openEditor(entry),
          },
        ]
      : []),
    {
      key: "rename",
      icon: <EditOutlined />,
      label: "Rename",
      onClick: () => openRename(entry),
    },
    {
      key: "permissions",
      icon: <LockOutlined />,
      label: "Permissions",
      onClick: () => openSingleChmod(entry),
    },
    {
      key: "delete",
      danger: true,
      icon: <DeleteOutlined />,
      label: "Delete",
      onClick: () => confirmDelete(entry),
    },
  ];

  // --- icon coloring ---
  // Folders are warm yellow (matches the AntD breadcrumb folder
  // glyph), generic files are blue, and recognised image files get
  // a dedicated picture-frame icon in magenta. Extension-only
  // detection is intentional — we don't want to read file bytes
  // just to colour the row.
  const IMAGE_EXTS = new Set([
    "jpg",
    "jpeg",
    "png",
    "gif",
    "webp",
    "svg",
    "bmp",
    "ico",
    "avif",
    "heic",
    "tiff",
    "tif",
  ]);
  const VIDEO_EXTS = new Set([
    "mp4",
    "mov",
    "mkv",
    "avi",
    "webm",
    "flv",
    "wmv",
    "m4v",
    "mpeg",
    "mpg",
    "ogv",
    "3gp",
  ]);
  const AUDIO_EXTS = new Set([
    "mp3",
    "wav",
    "flac",
    "ogg",
    "oga",
    "m4a",
    "aac",
    "wma",
    "opus",
    "aiff",
    "aif",
  ]);
  const ARCHIVE_EXTS = new Set([
    "zip",
    "tar",
    "gz",
    "tgz",
    "bz2",
    "tbz",
    "tbz2",
    "xz",
    "txz",
    "7z",
    "rar",
    "lz",
    "lzma",
    "zst",
    "zstd",
  ]);
  const CODE_EXTS = new Set([
    "php",
    "phtml",
    "html",
    "htm",
    "xhtml",
  ]);
  const STYLE_EXTS = new Set(["css", "scss", "sass", "less"]);
  const TEXT_EXTS = new Set([
    "txt",
    "md",
    "log",
    "rst",
    "readme",
    "rtf",
  ]);
  const renderEntryIcon = (entry: FileEntry) => {
    if (entry.is_dir) {
      return <FolderOutlined style={{ color: "#faad14", fontSize: 18 }} />;
    }
    const dot = entry.name.lastIndexOf(".");
    const ext = dot >= 0 ? entry.name.slice(dot + 1).toLowerCase() : "";
    if (IMAGE_EXTS.has(ext)) {
      return <FileImageOutlined style={{ color: "#eb2f96", fontSize: 18 }} />;
    }
    if (VIDEO_EXTS.has(ext)) {
      return <FilePlayOutlined style={{ color: "#722ed1", fontSize: 18 }} />;
    }
    if (AUDIO_EXTS.has(ext)) {
      return <FileVolumeOutlined style={{ color: "#1890ff", fontSize: 18 }} />;
    }
    if (ARCHIVE_EXTS.has(ext)) {
      return <FileArchiveOutlined style={{ color: "#a0522d", fontSize: 18 }} />;
    }
    if (CODE_EXTS.has(ext)) {
      return <FileCodeOutlined style={{ color: "#52c41a", fontSize: 18 }} />;
    }
    if (STYLE_EXTS.has(ext)) {
      return <FileMinusOutlined style={{ color: "#13c2c2", fontSize: 18 }} />;
    }
    if (TEXT_EXTS.has(ext)) {
      return <FileTextOutlined style={{ color: "#fa8c16", fontSize: 18 }} />;
    }
    return <FileOutlined style={{ color: "#1677ff", fontSize: 18 }} />;
  };

  // --- table columns ---
  const columns = [
    {
      title: "Name",
      dataIndex: "name",
      key: "name",
      render: (_: string, entry: FileEntry) => {
        const clickable = entry.is_dir || isImagePath(entry.name);
        return (
          <Space
            size={8}
            style={{ cursor: clickable ? "pointer" : "default" }}
            onClick={() => {
              if (entry.is_dir) {
                handleRowClick(entry);
              } else if (isImagePath(entry.name)) {
                void handlePreview(entry);
              }
            }}
          >
            {renderEntryIcon(entry)}
            <Text>{entry.name}</Text>
            {entry.is_symlink && <Text type="secondary">(link)</Text>}
          </Space>
        );
      },
    },
    {
      title: "Size",
      dataIndex: "size",
      key: "size",
      width: 120,
      render: (_: number, entry: FileEntry) => (entry.is_dir ? "—" : formatBytes(entry.size)),
    },
    {
      title: "Perms",
      dataIndex: "mode",
      key: "mode",
      width: 80,
      render: (v: string) => {
        const oct = symbolicModeToOctal(v);
        if (!oct) return "—";
        return (
          <Tag
            style={{
              fontFamily: "monospace",
              cursor: "default",
              marginInlineEnd: 0,
            }}
          >
            {oct}
          </Tag>
        );
      },
    },
    {
      title: "Modified",
      dataIndex: "mod_time",
      key: "mod_time",
      width: 180,
      render: (v: string) => formatModTime(v),
    },
    {
      title: "",
      key: "actions",
      width: 60,
      render: (_: unknown, entry: FileEntry) => (
        <Dropdown trigger={["click"]} menu={{ items: buildRowMenuItems(entry) }}>
          <Button type="text" icon={<MoreOutlined />} />
        </Dropdown>
      ),
    },
  ];

  // --- render ---
  if (!rootPath || !currentPath) {
    return (
      <div style={{ display: "flex", justifyContent: "center", padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }

  return (
    <div className="jabali-file-manager" style={{ padding: 16 }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          marginBottom: 12,
          gap: 16,
          flexWrap: "wrap",
        }}
      >
        <Breadcrumb
          items={crumbs.map((c) => ({
            title: (
              <a
                onClick={(e) => {
                  e.preventDefault();
                  navigateToPath(c.path);
                }}
              >
                {c.title}
              </a>
            ),
          }))}
        />
        <Space>
          {clipboard.length > 0 && (
            <Button
              type="primary"
              ghost
              onClick={() => void handlePaste()}
            >
              Paste ({clipboard.length})
            </Button>
          )}
          <Button
            icon={<UploadOutlined />}
            onClick={() => setUploadDrawerOpen(true)}
          >
            Upload
          </Button>
          <Dropdown
            trigger={["click"]}
            menu={{
              items: [
                {
                  key: "folder",
                  icon: (
                    <FolderAddOutlined style={{ color: "#faad14" }} />
                  ),
                  label: "New Folder",
                  onClick: openMkdir,
                },
                {
                  key: "file",
                  icon: <FileAddOutlined />,
                  label: "New File",
                  onClick: openNewFile,
                },
              ],
            }}
          >
            <Button type="primary" icon={<PlusOutlined />}>
              New <DownOutlined />
            </Button>
          </Dropdown>
          <Button icon={<ReloadOutlined />} onClick={handleRefresh} />
        </Space>
      </div>

      {/*
        Bulk-action bar: only rendered when at least one row is checked.
        Sits above the table so the N-count stays visible while the user
        confirms the action in a modal. Clear button mirrors the Esc
        shortcut for mouse users.
      */}
      {selectedNames.length > 0 && (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 12,
            marginBottom: 12,
            padding: "8px 12px",
            // Theme-tokenised surface so dark mode reads correctly. The
            // Primary-Bg/Border tokens give us a tinted-but-subtle bar
            // in both palettes (light: pale blue; dark: deep slate).
            background: token.colorPrimaryBg,
            border: `1px solid ${token.colorPrimaryBorder}`,
            borderRadius: token.borderRadius,
            color: token.colorText,
            // Sticky so bulk actions stay reachable even when scrolled
            // deep into a long folder. top: 0 pins to the viewport top;
            // zIndex keeps it above table rows that may have the row
            // selection highlight. The page itself scrolls on the body,
            // so `sticky` binds to the body scroll container.
            position: "sticky",
            top: 0,
            zIndex: 10,
          }}
        >
          <Text strong>
            {selectedNames.length} selected
          </Text>
          <Button
            icon={<DownloadOutlined />}
            onClick={() => void handleBulkDownloadZip()}
          >
            Download
          </Button>
          <Button icon={<CopyOutlined />} onClick={handleCopyToClipboard}>
            Copy
          </Button>
          <Button icon={<FolderInputOutlined />} onClick={() => setBulkMoveOpen(true)}>
            Move to…
          </Button>
          <Button icon={<LockOutlined />} onClick={() => setBulkChmodOpen(true)}>
            Permissions
          </Button>
          <Button icon={<DeleteOutlined />} danger onClick={handleBulkDelete}>
            Delete
          </Button>
          <Button type="text" icon={<CloseOutlined />} onClick={() => setSelectedNames([])}>
            Clear
          </Button>
        </div>
      )}

      {!inlineTree && (
        <Button
          icon={<FolderOutlined />}
          onClick={() => setTreeDrawerOpen(true)}
          style={{ marginBottom: 12 }}
        >
          Folders
        </Button>
      )}

      <UploadDrawer
        ref={uploadDrawerRef}
        open={uploadDrawerOpen}
        currentPath={currentPath ?? ""}
        onClose={() => setUploadDrawerOpen(false)}
        onOpenRequest={() => setUploadDrawerOpen(true)}
        onUploaded={() => {
          if (currentPath) void reloadList(currentPath);
        }}
      />

      <Drawer
        open={!inlineTree && treeDrawerOpen}
        onClose={() => setTreeDrawerOpen(false)}
        placement="left"
        width={280}
        title={t("filemanagerpage.folders")}
        styles={{ body: { padding: 8, background: token.colorBgContainer } }}
      >
        <Tree
          treeData={treeData}
          expandedKeys={expandedKeys}
          onExpand={(keys) => setExpandedKeys(keys as string[])}
          selectedKeys={[currentPath]}
          loadData={(node) => loadTreeChildren(node as TreeNode)}
          showLine
          switcherIcon={({ expanded }: { expanded?: boolean }) =>
            expanded ? (
              <FolderOpenOutlined style={{ color: "#faad14", fontSize: 18 }} />
            ) : (
              <FolderAddOutlined style={{ color: "#faad14", fontSize: 18 }} />
            )
          }
          onSelect={(keys) => {
            if (keys.length > 0) {
              const key = keys[0] as string;
              navigateToPath(key);
              setExpandedKeys((prev) =>
                prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]
              );
            }
          }}
        />
      </Drawer>

      <div style={{ display: "flex", gap: 16, alignItems: "stretch" }}>
        {inlineTree && (
        <Card
          title={<Typography.Text type="secondary">Folders</Typography.Text>}
          style={{ width: 320, flexShrink: 0 }}
          // body takes the scrollable region; the header is fixed so the
          // "Folders" title stays visible even when a deep tree scrolls.
          // Header bg matches the AntD Table header tint so the two side-by-side
          // panels read as one unit.
          styles={{
            header: {
              background: token.colorFillAlter,
            },
            body: {
              padding: 8,
              // Long folder names overflow the fixed-width tree pane.
              // Allow horizontal scroll instead of clipping under the
              // table card so the operator can pan to read them.
              overflowX: "auto",
            },
          }}
        >
          <Tree
            treeData={treeData}
            expandedKeys={expandedKeys}
            onExpand={(keys) => setExpandedKeys(keys as string[])}
            selectedKeys={[currentPath]}
            loadData={(node) => loadTreeChildren(node as TreeNode)}
            // Chevron switcher + dotted connector lines between siblings —
            // matches the AntD showLine pattern from the docs. The chevron
            // rotates based on expansion state automatically, so a single
            // DownOutlined serves both open and closed.
            showLine
            switcherIcon={({ expanded }: { expanded?: boolean }) =>
              expanded ? (
                <FolderOpenOutlined style={{ color: "#faad14", fontSize: 18 }} />
              ) : (
                <FolderAddOutlined style={{ color: "#faad14", fontSize: 18 }} />
              )
            }
            onSelect={(keys) => {
              if (keys.length > 0) {
                const key = keys[0] as string;
                navigateToPath(key);
                setExpandedKeys((prev) =>
                  prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]
                );
              }
            }}
            // Tree nodes accept drops of table rows (move into this folder).
            // Hits the same handleMove path as the table row-on-row drop.
            // We attach handlers via `titleRender` so every node in the tree
            // becomes a drop target without opting-in AntD's own tree DnD
            // (which is for reordering nodes — not what we want).
            titleRender={(node) => {
              const treeNode = node as TreeNode;
              return (
                <span
                  onDragOver={(e) => {
                    if (!e.dataTransfer.types.includes(dragPathMime)) return;
                    e.preventDefault();
                    e.dataTransfer.dropEffect = "move";
                  }}
                  onDrop={(e) => {
                    const raw = e.dataTransfer.getData(dragPathMime);
                    if (!raw) return;
                    e.preventDefault();
                    e.stopPropagation();
                    const paths = parseDragPayload(raw);
                    if (paths.length === 1) {
                      void handleMove(paths[0], treeNode.path);
                    } else if (paths.length > 1) {
                      void runBulk("Moved", paths, (p) => filesMove(p, treeNode.path));
                    }
                  }}
                >
                  {treeNode.title as ReactNode}
                </span>
              );
            }}
          />
        </Card>
        )}

        <Card
          style={{ flex: 1, minWidth: 0 }}
          styles={{ body: { padding: 0 } }}
          // OS-file drop zone: dragging files in from the desktop/Finder
          // uploads them to the current folder. Custom DataTransfer types
          // (from row drags) are filtered out so we only preventDefault on
          // real OS-file drags — otherwise AntD Table's internal row drag
          // would be swallowed by this parent handler.
          onDragOver={(e) => {
            if (e.dataTransfer.types.includes("Files")) e.preventDefault();
          }}
          onDrop={(e) => {
            if (!e.dataTransfer.types.includes("Files")) return;
            e.preventDefault();
            if (!currentPath) return;
            for (const f of Array.from(e.dataTransfer.files)) {
              enqueueUpload(f);
            }
          }}
        >
          <Spin spinning={listLoading}>
            <Table<FileEntry>
              rowKey="name"
              dataSource={entries}
              columns={columns as never}
              pagination={false}
              scroll={{ x: "max-content" }}
              locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("filemanagerpage.empty_directory")} /> }}
              // Row drag-to-move: any row is draggable; folders are drop
              // targets. `dragPathMime` carries the list of paths being
              // dragged (array, JSON-encoded). If the dragged row is part
              // of the current selection, drag the WHOLE selection as a
              // unit — dropping anywhere moves all selected rows. If the
              // dragged row is NOT selected, it's an individual drag and
              // the selection is untouched.
              rowSelection={{
                selectedRowKeys: selectedNames,
                onChange: (keys) => setSelectedNames(keys as string[]),
                columnWidth: 40,
              }}
              onRow={(entry) => ({
                draggable: true,
                onContextMenu: (e) => {
                  e.preventDefault();
                  setCtxMenu({ x: e.clientX, y: e.clientY, entry });
                },
                onDragStart: (e) => {
                  if (!currentPath) return;
                  const p = joinPath(currentPath, entry.name);
                  // If this row is part of the current selection, drag
                  // the whole selection. Otherwise drag just this row.
                  const paths = selectedNames.includes(entry.name)
                    ? selectedNames.map((n) => joinPath(currentPath, n))
                    : [p];
                  setDraggedPath(p);
                  e.dataTransfer.setData(dragPathMime, JSON.stringify(paths));
                  e.dataTransfer.effectAllowed = "move";
                },
                onDragOver: (e) => {
                  if (!entry.is_dir) return;
                  if (!e.dataTransfer.types.includes(dragPathMime)) return;
                  e.preventDefault();
                  e.dataTransfer.dropEffect = "move";
                },
                onDrop: (e) => {
                  if (!entry.is_dir || !currentPath) return;
                  const raw = e.dataTransfer.getData(dragPathMime);
                  if (!raw) return;
                  e.preventDefault();
                  e.stopPropagation();
                  const destDir = joinPath(currentPath, entry.name);
                  const paths = parseDragPayload(raw);
                  if (paths.length === 1) {
                    void handleMove(paths[0], destDir);
                  } else if (paths.length > 1) {
                    void runBulk("Moved", paths, (p) => filesMove(p, destDir));
                  }
                },
                onDragEnd: () => setDraggedPath(null),
                style:
                  draggedPath && draggedPath === joinPath(currentPath || "", entry.name)
                    ? { opacity: 0.4 }
                    : undefined,
              })}
            />
          </Spin>
        </Card>
      </div>

      {ctxMenu && (
        <Dropdown
          open
          trigger={["contextMenu"]}
          menu={{
            items: buildRowMenuItems(ctxMenu.entry),
            onClick: () => setCtxMenu(null),
          }}
          onOpenChange={(o) => { if (!o) setCtxMenu(null); }}
        >
          <div
            style={{
              position: "fixed",
              left: ctxMenu.x,
              top: ctxMenu.y,
              width: 1,
              height: 1,
              pointerEvents: "none",
            }}
          />
        </Dropdown>
      )}

      <Modal
        title={t("filemanagerpage.new_folder")}
        open={mkdirOpen}
        onOk={() => void submitMkdir()}
        onCancel={() => setMkdirOpen(false)}
        okText={t("filemanagerpage.create")}
      >
        <Input
          value={mkdirName}
          onChange={(e) => setMkdirName(e.target.value)}
          placeholder="folder-name"
          autoFocus
          onPressEnter={() => void submitMkdir()}
        />
      </Modal>

      <Modal
        title={t("filemanagerpage.new_file")}
        open={newFileOpen}
        onOk={() => void submitNewFile()}
        onCancel={() => setNewFileOpen(false)}
        okText={t("filemanagerpage.create")}
      >
        <Input
          value={newFileName}
          onChange={(e) => setNewFileName(e.target.value)}
          placeholder="filename.txt"
          autoFocus
          onPressEnter={() => void submitNewFile()}
        />
      </Modal>

      <Modal
        title={`Rename ${renameTarget?.name ?? ""}`}
        open={renameOpen}
        onOk={() => void submitRename()}
        onCancel={() => setRenameOpen(false)}
        okText={t("filemanagerpage.rename")}
      >
        <Input
          value={renameNewName}
          onChange={(e) => setRenameNewName(e.target.value)}
          autoFocus
          onPressEnter={() => void submitRename()}
        />
      </Modal>

      <Modal
        title={previewPath}
        open={previewOpen}
        onCancel={() => setPreviewOpen(false)}
        width="min(900px, 90vw)"
        footer={null}
      >
        <Spin spinning={previewLoading}>
          {previewPath && isImagePath(previewPath) ? (
            <div style={{ textAlign: "center", maxHeight: "70vh", overflow: "auto" }}>
              <img
                src={filesDownloadURL(previewPath)}
                alt={previewPath}
                style={{ maxWidth: "100%", maxHeight: "65vh" }}
              />
            </div>
          ) : (
            <pre
              style={{
                maxHeight: "60vh",
                overflow: "auto",
                background: token.colorFillQuaternary,
                color: token.colorText,
                padding: 12,
                borderRadius: token.borderRadius,
                whiteSpace: "pre-wrap",
                wordBreak: "break-all",
              }}
            >
              {previewContent}
            </pre>
          )}
        </Spin>
      </Modal>

      <Modal
        title={`Move ${selectedPaths.length} item${selectedPaths.length === 1 ? "" : "s"}`}
        open={bulkMoveOpen}
        onOk={() => void handleBulkMove()}
        onCancel={() => {
          setBulkMoveOpen(false);
          setBulkMoveDest("");
        }}
        okText={t("filemanagerpage.move")}
        okButtonProps={{ disabled: !bulkMoveDest }}
      >
        <p style={{ marginBottom: 8 }}>Pick a destination folder:</p>
        <div
          style={{
            maxHeight: 360,
            overflow: "auto",
            border: `1px solid ${token.colorBorderSecondary}`,
            borderRadius: token.borderRadius,
            padding: 8,
          }}
        >
          {/*
            Reuses the same treeData + expandedKeys + lazy loader as the
            left sidebar tree, so folders the user already expanded over
            there stay expanded here. onSelect sets the destination;
            titleRender is omitted — the modal tree is a picker, not a
            drop target, and the tree-node drop handlers would swallow
            clicks on nested folders.
          */}
          <Tree
            treeData={treeData}
            expandedKeys={expandedKeys}
            onExpand={(keys) => setExpandedKeys(keys as string[])}
            selectedKeys={bulkMoveDest ? [bulkMoveDest] : []}
            loadData={(node) => loadTreeChildren(node as TreeNode)}
            showLine
            switcherIcon={({ expanded }: { expanded?: boolean }) =>
              expanded ? (
                <FolderOpenOutlined style={{ color: "#faad14", fontSize: 18 }} />
              ) : (
                <FolderAddOutlined style={{ color: "#faad14", fontSize: 18 }} />
              )
            }
            onSelect={(keys) => {
              if (keys.length > 0) setBulkMoveDest(keys[0] as string);
            }}
          />
        </div>
        <div
          style={{
            marginTop: 8,
            color: bulkMoveDest ? token.colorText : token.colorTextTertiary,
            fontFamily: "monospace",
          }}
        >
          {bulkMoveDest || "(no folder selected)"}
        </div>
      </Modal>

      <Modal
        title={`Change permissions on ${selectedPaths.length} item${selectedPaths.length === 1 ? "" : "s"}`}
        open={bulkChmodOpen}
        onOk={() => void handleBulkChmod()}
        onCancel={() => setBulkChmodOpen(false)}
        okText={t("filemanagerpage.apply")}
      >
        <ChmodEditor value={bulkChmodMode} onChange={setBulkChmodMode} />
      </Modal>

      <Modal
        title={chmodTarget ? `Permissions — ${chmodTarget.name}` : "Permissions"}
        open={!!chmodTarget}
        onOk={() => void submitSingleChmod()}
        onCancel={() => setChmodTarget(null)}
        okText={t("filemanagerpage.apply")}
      >
        <ChmodEditor value={chmodTargetMode} onChange={setChmodTargetMode} />
      </Modal>

      <Modal
        title={editTarget ? `Edit — ${editTarget.split("/").pop()}` : "Edit"}
        open={!!editTarget}
        onOk={() => void submitEdit()}
        onCancel={() => setEditTarget(null)}
        okText={t("filemanagerpage.save")}
        okButtonProps={{ loading: editSaving, disabled: editContent === editOriginal }}
        width="min(1100px, 95vw)"
      >
        <Spin spinning={editLoading}>
          <div
            style={{
              height: "65vh",
              border: `1px solid ${token.colorBorderSecondary}`,
              borderRadius: token.borderRadius,
              overflow: "hidden",
            }}
          >
            <Suspense
              fallback={
                <div
                  style={{
                    height: "100%",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                  }}
                >
                  <Spin tip="Loading editor…" />
                </div>
              }
            >
              <Editor
                height="100%"
                value={editContent}
                onChange={(v) => setEditContent(v ?? "")}
                language={editTarget ? languageFor(editTarget) : "plaintext"}
                theme={
                  // Pick vs-dark when the app is in dark mode, vs when light.
                  // AntD's token exposes colorBgBase; a dark bg implies dark.
                  token.colorBgBase && token.colorBgBase.startsWith("#0")
                    ? "vs-dark"
                    : "vs"
                }
                options={{
                  minimap: { enabled: false },
                  scrollBeyondLastLine: false,
                  fontSize: 13,
                }}
              />
            </Suspense>
          </div>
        </Spin>
      </Modal>
    </div>
  );
};

// ChmodEditor — two-way bound rwx-checkbox grid + octal text input.
// Typing in the box updates the checkboxes when the input parses, and
// toggling checkboxes rewrites the box. Accepts only 3- or 4-digit octal
// strings; anything else leaves the checkboxes where they were.
function ChmodEditor({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const bits = parseOctalToBits(value);
  const setBit = (i: number, b: boolean) => {
    const next = [...bits];
    next[i] = b;
    onChange(bitsToOctal(next));
  };
  const labels = ["Owner", "Group", "Other"];
  return (
    <div>
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "80px repeat(3, auto)",
          gap: 8,
          alignItems: "center",
          marginBottom: 12,
        }}
      >
        <div />
        <Text strong>Read</Text>
        <Text strong>Write</Text>
        <Text strong>Exec</Text>
        {labels.map((lab, row) => (
          <Row key={lab} lab={lab} row={row} bits={bits} setBit={setBit} />
        ))}
      </div>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="0644"
        style={{ fontFamily: "monospace" }}
        maxLength={4}
      />
    </div>
  );
}

function Row({
  lab,
  row,
  bits,
  setBit,
}: {
  lab: string;
  row: number;
  bits: boolean[];
  setBit: (i: number, b: boolean) => void;
}) {
  // row=0 → owner bits at 0..2; row=1 → group at 3..5; row=2 → other at 6..8
  const base = row * 3;
  return (
    <>
      <Text>{lab}</Text>
      {[0, 1, 2].map((col) => (
        <input
          key={col}
          type="checkbox"
          checked={bits[base + col] || false}
          onChange={(e) => setBit(base + col, e.target.checked)}
        />
      ))}
    </>
  );
}

// parseOctalToBits: "0755" → [r,w,x, r,-,x, r,-,x] flattened as 9 booleans.
// Returns all-false on an unparseable string so the editor stays open.
function parseOctalToBits(s: string): boolean[] {
  const trimmed = s.trim().replace(/^0o/i, "");
  if (!/^[0-7]{3,4}$/.test(trimmed)) return new Array(9).fill(false);
  // Use the low 3 digits — the setuid/setgid/sticky digit is ignored by
  // this editor's UI (it's in the Advanced octal box).
  const rwx = trimmed.slice(-3);
  const bits: boolean[] = [];
  for (const ch of rwx) {
    const n = parseInt(ch, 8);
    bits.push((n & 4) !== 0, (n & 2) !== 0, (n & 1) !== 0);
  }
  return bits;
}

function bitsToOctal(bits: boolean[]): string {
  let out = "0";
  for (let i = 0; i < 3; i++) {
    let d = 0;
    if (bits[i * 3 + 0]) d |= 4;
    if (bits[i * 3 + 1]) d |= 2;
    if (bits[i * 3 + 2]) d |= 1;
    out += d.toString(8);
  }
  return out;
}

// updateTreeNode replaces the children of a tree node at `path` (immutably).
function updateTreeNode(nodes: TreeNode[], path: string, children: TreeNode[]): TreeNode[] {
  return nodes.map((n) => {
    if (n.path === path) return { ...n, children };
    if (n.children) {
      return { ...n, children: updateTreeNode(n.children as TreeNode[], path, children) };
    }
    return n;
  });
}

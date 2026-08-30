// filesApi.ts — typed wrappers around the /api/v1/files endpoints.
import { apiClient } from "../../../apiClient";
import { getActAs } from "../../../impersonation";

export type FileEntry = {
  name: string;
  is_dir: boolean;
  size: number;
  mode: string;
  mod_time: string;
  is_symlink: boolean;
  // Only meaningful for is_dir entries; absent/false for files. Drives
  // the tree's chevron visibility — a folder with no subfolders is
  // rendered as a leaf (no expand arrow).
  has_subdirs?: boolean;
};

export type FileListResponse = {
  path: string;
  entries: FileEntry[];
};

export type FilePreviewResponse = {
  path: string;
  size: number;
  content: string;
  // Server-sniffed content type (Go's http.DetectContentType on first 512 B).
  // Used by the editor to refuse binary files before they land in Monaco —
  // loading a 1 MiB JPEG into a text editor is a mess the user shouldn't see.
  mime_type?: string;
  // JAB-191: true when the bytes are not editable text -- either sniffed binary,
  // or text that is not valid UTF-8 (latin1/windows-1252 PHP and HTML, common on
  // legacy sites). Those come back with an EMPTY content field because the bytes
  // travelled as base64, so the editor must refuse rather than open a blank
  // buffer and save that blank over the user's file.
  is_binary?: boolean;
};

export async function filesHome(): Promise<{ path: string }> {
  const r = await apiClient.get<{ path: string }>("/files/home");
  return r.data;
}

export async function filesList(path: string): Promise<FileListResponse> {
  const r = await apiClient.get<FileListResponse>("/files", { params: { path } });
  return r.data;
}

export async function filesTree(path: string): Promise<FileListResponse> {
  const r = await apiClient.get<FileListResponse>("/files/tree", { params: { path } });
  return r.data;
}

export async function filesPreview(path: string): Promise<FilePreviewResponse> {
  const r = await apiClient.get<FilePreviewResponse>("/files/preview", {
    params: { path },
  });
  return r.data;
}

export function filesDownloadURL(path: string): string {
  // window.open() and <img src> can't set the X-Jabali-Act-As header the
  // apiClient interceptor injects, so an admin impersonating a tenant would
  // otherwise hit /files/download as themselves and get not_in_scope. The
  // backend honors ?act_as on GET (ResolveImpersonation), so carry the active
  // grant in the query string — mirrors MyProfileBackupCard's download.
  const actAs = getActAs();
  const base = `/api/v1/files/download?path=${encodeURIComponent(path)}`;
  return actAs ? `${base}&act_as=${encodeURIComponent(actAs.id)}` : base;
}

export interface UploadOpts {
  overwrite?: boolean;
  // name overrides the destination filename (used for "keep both" auto-rename
  // on a collision). Defaults to the File's own name.
  name?: string;
}

export async function filesUpload(
  dirPath: string,
  file: File,
  onProgress?: (frac: number) => void,
  opts?: UploadOpts,
  base = "/files",
): Promise<void> {
  const fd = new FormData();
  fd.append("file", file);
  const q = new URLSearchParams({ path: dirPath });
  if (opts?.overwrite) q.set("overwrite", "true");
  if (opts?.name) q.set("name", opts.name);
  await apiClient.post(`${base}/upload?${q.toString()}`, fd, {
    headers: { "Content-Type": "multipart/form-data" },
    onUploadProgress: (e) => {
      if (!onProgress) return;
      const total = e.total ?? file.size;
      if (total > 0) onProgress(Math.min(1, e.loaded / total));
    },
  });
}

// filesUploadChunked — chunked upload for files > 100 MB. Sends the
// file as N sequential POSTs of `chunkSize` bytes each, the last one
// flagged `final=1` so the backend finalises (moves /tmp into scope).
// `onProgress` is called with a 0..1 fraction after each chunk.
//
// Resumable: if a previous upload for the same file (keyed by dir + name
// + size + lastModified) was interrupted, we reuse that upload_id and
// ask the server how many bytes landed, then skip ahead to the next
// chunk boundary. The key lives in localStorage under `jabali:upload:`
// and is cleaned on successful ingest.
export async function filesUploadChunked(
  dirPath: string,
  file: File,
  chunkSize = 10 * 1024 * 1024,
  onProgress?: (frac: number) => void,
  opts?: UploadOpts,
  base = "/files",
): Promise<void> {
  const destName = opts?.name ?? file.name;
  const totalChunks = Math.max(1, Math.ceil(file.size / chunkSize));
  const resumeKey = `jabali:upload:${dirPath}|${destName}|${file.size}|${file.lastModified}`;
  let uploadId = readResumeId(resumeKey);
  let startChunk = 0;
  if (uploadId) {
    // See how much the server has already. If 404, the /tmp file is
    // gone (panel restart, cleanup job) and we start fresh.
    try {
      const r = await apiClient.get<{ written: number }>(
        `${base}/upload-chunk-status`,
        { params: { upload_id: uploadId } },
      );
      const written = r.data.written || 0;
      // Resume at the start of the first not-yet-complete chunk. Round
      // DOWN so a partial chunk is re-uploaded in full — the server
      // seeks to the offset before writing, so re-sending is safe.
      startChunk = Math.floor(written / chunkSize);
    } catch {
      // Stale or missing — drop the key and regenerate.
      uploadId = null;
      clearResumeId(resumeKey);
    }
  }
  if (!uploadId) {
    uploadId =
      typeof crypto !== "undefined" && "randomUUID" in crypto
        ? crypto.randomUUID()
        : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    writeResumeId(resumeKey, uploadId);
  }
  if (onProgress) onProgress(startChunk / totalChunks);
  for (let i = startChunk; i < totalChunks; i++) {
    const start = i * chunkSize;
    const end = Math.min(start + chunkSize, file.size);
    const blob = file.slice(start, end);
    const isLast = i === totalChunks - 1;
    const params = new URLSearchParams({
      upload_id: uploadId,
      offset: String(start),
      path: dirPath,
      name: destName,
      ...(isLast ? { final: "1" } : {}),
      ...(isLast && opts?.overwrite ? { overwrite: "true" } : {}),
    });
    await apiClient.post(`${base}/upload-chunk?${params.toString()}`, blob, {
      headers: { "Content-Type": "application/octet-stream" },
    });
    if (onProgress) onProgress((i + 1) / totalChunks);
  }
  // Success — forget the resume key.
  clearResumeId(resumeKey);
}

// Small localStorage helpers. Wrapped in try/catch because the SPA can
// be loaded in a privacy-mode browser where setItem throws — we still
// want the upload to work, just without resume.
function readResumeId(key: string): string | null {
  try {
    return typeof localStorage !== "undefined" ? localStorage.getItem(key) : null;
  } catch {
    return null;
  }
}

function writeResumeId(key: string, id: string): void {
  try {
    if (typeof localStorage !== "undefined") localStorage.setItem(key, id);
  } catch {
    // Best-effort; we tolerate losing resume state.
  }
}

function clearResumeId(key: string): void {
  try {
    if (typeof localStorage !== "undefined") localStorage.removeItem(key);
  } catch {
    // Best-effort.
  }
}

// filesWrite overwrites the content of an existing file (or creates it
// if missing) with the given UTF-8 string. Powers the Monaco editor's
// Save action — binary-safe reads/writes are a Phase-3 concern.
export async function filesWrite(path: string, content: string): Promise<void> {
  await apiClient.post("/files/write", { path, content });
}

export async function filesMkdir(path: string): Promise<void> {
  await apiClient.post("/files/mkdir", { path });
}

export interface FilesExtractResult {
  dest: string;
  extracted: number;
  skipped: number;
}

// filesExtract unpacks an archive (.zip/.tar/.tar.gz/.tgz/.tar.bz2/.gz) into
// dest (default: the archive's own directory). The agent enforces zip-slip,
// symlink, and decompression-bomb defenses.
export async function filesExtract(
  path: string,
  dest?: string,
): Promise<FilesExtractResult> {
  const { data } = await apiClient.post<FilesExtractResult>("/files/extract", {
    path,
    dest,
  });
  return data;
}

// GH #1392: async extract. filesExtractStart kicks off the extraction as a
// background job on the agent and returns immediately with a job id (HTTP 202);
// the caller polls filesJobStatus for a progress bar. Same defenses as the
// blocking filesExtract — the agent runs the identical extract core.
export async function filesExtractStart(
  path: string,
  dest?: string,
): Promise<{ job_id: string }> {
  const { data } = await apiClient.post<{ job_id: string }>(
    "/files/extract",
    { path, dest },
    { params: { async: 1 } },
  );
  return data;
}

export interface FilesJobStatus {
  job_id: string;
  status: "running" | "done" | "error";
  done: number;
  total: number; // 0 = unknown (streamed tar) → indeterminate progress
  result: FilesExtractResult;
  error?: string;
  started_at: string;
}

// filesJobStatus polls a background file-operation job (GH #1392). The backend
// returns the job only to the tenant that started it; an unknown/foreign id is
// a 404 (surfaced as a rejected promise), which the poller treats as "the job
// is gone — refresh the folder".
export async function filesJobStatus(jobId: string): Promise<FilesJobStatus> {
  const { data } = await apiClient.get<FilesJobStatus>(
    `/files/jobs/${encodeURIComponent(jobId)}`,
  );
  return data;
}

export async function filesRename(path: string, newName: string): Promise<void> {
  await apiClient.post("/files/rename", { path, new_name: newName });
}

// filesMove relocates a file or directory into a different parent
// directory. Distinct from rename (same-parent only). Powers the
// drag-and-drop flow — dragging a row onto a folder row moves the
// source into that folder, preserving the basename.
export async function filesMove(path: string, destDir: string): Promise<void> {
  await apiClient.post("/files/move", { path, dest_dir: destDir });
}

// filesChmod sets Unix permission bits on a single file or directory.
// `mode` is a 3- or 4-digit octal string ("755", "0644", "1777"); the
// agent parses + masks to the low 12 bits. Bulk chmod from the UI
// loops this per entry so per-item failures surface individually.
export async function filesChmod(path: string, mode: string): Promise<void> {
  await apiClient.post("/files/chmod", { path, mode });
}

// filesCopy recursively copies a scoped path into a different parent
// directory, preserving mode and symlink targets. Basename preserved
// server-side — the caller sends the destination *folder*, not the
// destination path.
export async function filesCopy(path: string, destDir: string): Promise<void> {
  await apiClient.post("/files/copy", { path, dest_dir: destDir });
}

// filesArchive posts the selection and streams back a tar.gz download.
// One request = one archive — the backend creates a scratch file, streams
// it out, and unlinks as part of the same round-trip. Returns the Blob
// so the caller can trigger a save-as on the user's machine.
export async function filesArchive(paths: string[]): Promise<Blob> {
  const r = await apiClient.post<Blob>(
    "/files/archive",
    { paths },
    { responseType: "blob" },
  );
  return r.data;
}

export async function filesDelete(path: string, recursive = false): Promise<void> {
  await apiClient.delete("/files", {
    params: { path, ...(recursive ? { recursive: "true" } : {}) },
  });
}

// filesDu computes REAL recursive sizes (GH #657) for a directory's children —
// unlike files.list, which reports a folder's inode size. On-demand only
// (expensive on large trees). Reuses the disk-usage endpoint, which runs the
// same filesafe-scoped `files.du` agent verb, so no new route is needed.
export type FilesDuEntry = {
  name: string;
  is_dir: boolean;
  size: number;
  has_subdirs: boolean;
};

export type FilesDuResponse = {
  path: string;
  total: number;
  entries: FilesDuEntry[];
};

export async function filesDu(path: string): Promise<FilesDuResponse> {
  const r = await apiClient.get<FilesDuResponse>("/me/disk-usage/files", {
    params: { path },
  });
  return r.data;
}

// GH #1184: bundle the whole surface into one object so FileManagerPage can be
// driven by an injected API — the tenant page passes this; the admin File
// Manager passes an /admin/files-backed object with the same shape. Keeps the
// two file managers byte-identical in layout/behaviour with no drift.
export type FilesApi = {
  home: typeof filesHome;
  list: typeof filesList;
  tree: typeof filesTree;
  preview: typeof filesPreview;
  downloadURL: typeof filesDownloadURL;
  upload: typeof filesUpload;
  uploadChunked: typeof filesUploadChunked;
  write: typeof filesWrite;
  mkdir: typeof filesMkdir;
  extract: typeof filesExtract;
  extractStart: typeof filesExtractStart;
  jobStatus: typeof filesJobStatus;
  rename: typeof filesRename;
  move: typeof filesMove;
  chmod: typeof filesChmod;
  copy: typeof filesCopy;
  archive: typeof filesArchive;
  delete: typeof filesDelete;
  du: typeof filesDu;
};

export const tenantFilesApi: FilesApi = {
  home: filesHome,
  list: filesList,
  tree: filesTree,
  preview: filesPreview,
  downloadURL: filesDownloadURL,
  upload: filesUpload,
  uploadChunked: filesUploadChunked,
  write: filesWrite,
  mkdir: filesMkdir,
  extract: filesExtract,
  extractStart: filesExtractStart,
  jobStatus: filesJobStatus,
  rename: filesRename,
  move: filesMove,
  chmod: filesChmod,
  copy: filesCopy,
  archive: filesArchive,
  delete: filesDelete,
  du: filesDu,
};

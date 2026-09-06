// UploadDrawer — multi-file upload UX.
//
// Click "Upload" on the FileManagerPage → this drawer slides in from the
// right. The drawer hosts:
//
//   1. AntD Upload.Dragger (drop zone + click-to-pick), multiple=true
//      so the user can queue several files at once.
//   2. A list of every queued file with per-file progress bar and
//      status (queued / uploading N% / done / error). Failed rows
//      keep the error message so the user can read what went wrong
//      without diving into DevTools.
//   3. Footer: total progress + "Close" / "Clear completed" actions.
//
// Concurrency: sequential. Two reasons: (a) the agent's UDS pipe
// serialises calls anyway, so two parallel uploads queue at the agent
// boundary with no real speed-up; (b) it keeps the per-file progress
// bar honest — the active file's bar moves while queued files sit at
// 0%, instead of all bars rising in lockstep.
//
// Path-routing rule: ≤100 MB → single-multipart /files/upload (xhr
// with onUploadProgress); >100 MB → /files/upload-chunk (10 MB chunks,
// resumable). Same split as the old inline implementation.
import {
  CheckCircleFilled,
  CloseCircleFilled,
  CloseOutlined,
  InboxOutlined,
} from "@ant-design/icons";
import { Button, Checkbox, Drawer, List, Modal, Progress, Space, Typography, Upload } from "antd";
import type { UploadProps } from "antd";
import { AxiosError } from "axios";
import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { getIdentity } from "../../../identity";
import { UPLOAD_CHUNK_BYTES, UPLOAD_SINGLE_SHOT_MAX } from "../../../apiClient";
import { isIPHost, tenantFilesApi, type FilesApi } from "./filesApi";
import type { UploadOpts } from "./filesApi";

export interface UploadDrawerHandle {
  /**
   * Enqueue a file for upload and open the drawer if closed. `relDir` is an
   * optional subfolder under currentPath (GH #1243 folder upload) — the caller
   * must have already created that directory tree.
   */
  enqueue: (file: File, relDir?: string) => void;
}

type UploadStatus = "queued" | "uploading" | "success" | "error" | "cancelled";

interface UploadItem {
  id: string;
  file: File;
  // relDir = subfolder under currentPath this file belongs in (GH #1243 folder
  // upload), e.g. "my-folder/assets". Empty for a plain file drop.
  relDir?: string;
  status: UploadStatus;
  progress: number;
  // GH #1410: smoothed upload speed in bytes/sec while uploading (undefined
  // before the first sample).
  speed?: number;
  errorMessage?: string;
}

interface UploadDrawerProps {
  open: boolean;
  currentPath: string;
  onClose: () => void;
  onUploaded: () => void;
  onOpenRequest: () => void;
  /** Injected file API — defaults to the tenant surface (GH #1184). */
  api?: FilesApi;
}

// GH #1410: match the DB restore upload — files up to UPLOAD_SINGLE_SHOT_MAX go
// as one request, larger ones chunk at UPLOAD_CHUNK_BYTES (80 MB). This is the
// bump the earlier fix missed: it changed filesApi's default but this call site
// still passed 10 MB, so uploads stayed on 10 MB chunks.
const SINGLE_MULTIPART_CEILING = UPLOAD_SINGLE_SHOT_MAX;
const CHUNK_SIZE = UPLOAD_CHUNK_BYTES;
const HARD_CEILING = 1024 * 1024 * 1024;

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function errMessage(err: unknown): string {
  if (err instanceof AxiosError) {
    const data = err.response?.data as { detail?: string; error?: string } | undefined;
    if (err.response?.status === 507 || data?.error === "quota_exceeded") {
      return "Disk quota exceeded";
    }
    if (data?.error === "disk_full") {
      return "Server disk full";
    }
    if (data?.error === "file_too_large") {
      return "File too large";
    }
    return data?.detail || data?.error || err.message;
  }
  if (err instanceof Error) return err.message;
  return "Unexpected error";
}

// isAlreadyExists detects the 409 a collision raises (GH #188) so the worker
// can offer overwrite / keep-both / cancel instead of just failing.
function isAlreadyExists(err: unknown): boolean {
  if (err instanceof AxiosError) {
    const data = err.response?.data as { error?: string } | undefined;
    return err.response?.status === 409 || data?.error === "already_exists";
  }
  return false;
}

// GH #1410: axios throws a CanceledError when an AbortSignal fires. Detect it by
// code/name so a user cancel reads as "cancelled", not an upload error.
function isCanceled(err: unknown): boolean {
  const e = err as { code?: string; name?: string } | undefined;
  return e?.code === "ERR_CANCELED" || e?.name === "CanceledError";
}

// makeRenamedName turns "photo.jpg" into "photo (1).jpg" for "keep both".
function makeRenamedName(name: string, n: number): string {
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return `${name} (${n})`; // no ext, or a leading-dot dotfile
  return `${name.slice(0, dot)} (${n})${name.slice(dot)}`;
}

type ConflictDecision = {
  action: "overwrite" | "keepboth" | "cancel";
  always: boolean;
};

export const UploadDrawer = forwardRef<UploadDrawerHandle, UploadDrawerProps>(function UploadDrawer(
  { open, currentPath, onClose, onUploaded, onOpenRequest, api = tenantFilesApi },
  ref,
) {
  const [items, setItems] = useState<UploadItem[]>([]);
  const [running, setRunning] = useState(false);
  // File-manager upload ceiling from server_settings.upload_max_size_mb
  // (#211). Defaults to 1 GB until /me resolves; admin raises it in
  // Server Settings and it takes effect on the next drawer mount.
  const [maxBytes, setMaxBytes] = useState<number>(HARD_CEILING);
  useEffect(() => {
    let alive = true;
    getIdentity().then((id) => {
      if (alive && id?.uploadMaxSizeMb && id.uploadMaxSizeMb > 0) {
        setMaxBytes(id.uploadMaxSizeMb * 1024 * 1024);
      }
    });
    return () => {
      alive = false;
    };
  }, []);
  // queue of items waiting to be processed; ref so the worker loop sees
  // the latest set without re-running on every state change
  const queueRef = useRef<UploadItem[]>([]);
  // GH #1410: cancel support. One AbortController per in-flight item + a set of
  // ids cancelled before they started (so a queued item that's already been
  // shifted into the worker still bails). Kept in refs so aborting never waits
  // on a re-render.
  const controllers = useRef<Map<string, AbortController>>(new Map());
  const cancelledIds = useRef<Set<string>>(new Set());
  // GH #1410: per-item speed sampler (last timestamp + bytes + smoothed value).
  const speedRef = useRef<Map<string, { t: number; bytes: number; ema: number }>>(new Map());
  // GH #1410: reached by a bare IP → no Cloudflare/proxy body cap in the path,
  // so large files can upload as one direct request instead of chunking.
  const isDirectHost = useMemo(
    () => (typeof window !== "undefined" ? isIPHost(window.location.hostname) : false),
    [],
  );
  const effectiveSingleShot = isDirectHost ? maxBytes : SINGLE_MULTIPART_CEILING;
  // Collision prompt (GH #188). conflictItem drives the modal; the worker
  // loop awaits the user's decision via the promise stashed in the ref.
  const [conflictItem, setConflictItem] = useState<UploadItem | null>(null);
  const [conflictAlways, setConflictAlways] = useState(false);
  const conflictResolveRef = useRef<((d: ConflictDecision) => void) | null>(null);
  const alwaysOverwriteRef = useRef(false);

  const askConflict = useCallback((item: UploadItem) => {
    setConflictAlways(false);
    setConflictItem(item);
    return new Promise<ConflictDecision>((resolve) => {
      conflictResolveRef.current = resolve;
    });
  }, []);

  const resolveConflict = useCallback(
    (action: ConflictDecision["action"]) => {
      const resolve = conflictResolveRef.current;
      conflictResolveRef.current = null;
      setConflictItem(null);
      resolve?.({ action, always: conflictAlways });
    },
    [conflictAlways],
  );

  const updateItem = useCallback((id: string, patch: Partial<UploadItem>) => {
    setItems((prev) =>
      prev.map((it) => (it.id === id ? { ...it, ...patch } : it)),
    );
  }, []);

  const runOne = useCallback(
    async (item: UploadItem) => {
      // Cancelled while still queued (before we got here) — nothing to do.
      if (cancelledIds.current.has(item.id)) {
        updateItem(item.id, { status: "cancelled" });
        return;
      }
      const controller = new AbortController();
      controllers.current.set(item.id, controller);
      speedRef.current.set(item.id, { t: performance.now(), bytes: 0, ema: 0 });
      let lastUpdate = 0;
      // Progress callback: derive a smoothed speed from the fraction delta and
      // throttle the state writes so a fast upload doesn't re-render the list on
      // every progress event.
      const onp = (frac: number) => {
        const now = performance.now();
        const loaded = frac * item.file.size;
        const s = speedRef.current.get(item.id);
        if (s) {
          const dt = (now - s.t) / 1000;
          if (dt >= 0.2) {
            const inst = Math.max(0, (loaded - s.bytes) / dt);
            s.ema = s.ema > 0 ? s.ema * 0.7 + inst * 0.3 : inst;
            s.t = now;
            s.bytes = loaded;
          }
        }
        if (now - lastUpdate >= 300 || frac >= 1) {
          lastUpdate = now;
          updateItem(item.id, { progress: frac, speed: s?.ema });
        }
      };
      try {
        if (item.file.size > maxBytes) {
          throw new Error(`exceeds the ${formatBytes(maxBytes)} upload limit`);
        }
        // GH #1243: a folder-upload file lands in currentPath/<relDir>. The
        // drop handler has already created that directory tree (mkdir -p).
        const destDir = item.relDir
          ? `${currentPath.replace(/\/+$/, "")}/${item.relDir}`
          : currentPath;
        // doUpload always injects the signal so the retry loop below can't drop
        // it. Over a bare IP a big file goes as one direct request (effective-
        // SingleShot = the server limit); over a domain it chunks past 90 MB.
        const doUpload = (opts?: UploadOpts) => {
          const merged: UploadOpts = { ...opts, signal: controller.signal };
          return item.file.size <= effectiveSingleShot
            ? api.upload(destDir, item.file, onp, merged)
            : api.uploadChunked(destDir, item.file, CHUNK_SIZE, onp, merged);
        };
        let mode: "ask" | "overwrite" | "rename" = alwaysOverwriteRef.current
          ? "overwrite"
          : "ask";
        let renameN = 0;
        for (let guard = 0; guard < 1002; guard++) {
          // A cancel can land between iterations (e.g. during the conflict
          // prompt, when no request is in flight for abort() to interrupt).
          if (controller.signal.aborted) {
            updateItem(item.id, { status: "cancelled" });
            return;
          }
          let opts: UploadOpts | undefined;
          if (mode === "overwrite") opts = { overwrite: true };
          else if (mode === "rename")
            opts = { name: makeRenamedName(item.file.name, ++renameN) };
          updateItem(item.id, { status: "uploading", progress: 0, speed: undefined });
          try {
            await doUpload(opts);
            updateItem(item.id, { status: "success", progress: 1, speed: undefined });
            return;
          } catch (err) {
            if (isCanceled(err)) {
              updateItem(item.id, { status: "cancelled" });
              return;
            }
            if (!isAlreadyExists(err)) throw err;
            if (mode === "rename") continue; // try the next (n) silently
            if (mode === "overwrite") throw err; // overwrite shouldn't collide
            const d = await askConflict(item);
            if (d.action === "cancel") {
              updateItem(item.id, {
                status: "error",
                errorMessage: "Skipped — file already exists",
              });
              return;
            }
            if (d.action === "overwrite") {
              if (d.always) alwaysOverwriteRef.current = true;
              mode = "overwrite";
              continue;
            }
            mode = "rename"; // keep both
          }
        }
        updateItem(item.id, {
          status: "error",
          errorMessage: "Too many name collisions",
        });
      } catch (err) {
        if (isCanceled(err)) {
          updateItem(item.id, { status: "cancelled" });
        } else {
          updateItem(item.id, { status: "error", errorMessage: errMessage(err) });
        }
      } finally {
        controllers.current.delete(item.id);
        speedRef.current.delete(item.id);
        cancelledIds.current.delete(item.id);
      }
    },
    [currentPath, updateItem, askConflict, maxBytes, effectiveSingleShot],
  );

  // GH #1410: cancel a queued or in-flight upload. A still-queued item is just
  // dropped from the queue (it never runs, so no cancelledIds entry to leak).
  // One already picked up by the worker is flagged + its request aborted; runOne
  // clears the flag in its finally.
  const cancelItem = useCallback((id: string) => {
    const wasQueued = queueRef.current.some((q) => q.id === id);
    queueRef.current = queueRef.current.filter((q) => q.id !== id);
    if (!wasQueued) {
      cancelledIds.current.add(id);
      controllers.current.get(id)?.abort();
    }
    setItems((prev) =>
      prev.map((it) =>
        it.id === id && (it.status === "queued" || it.status === "uploading")
          ? { ...it, status: "cancelled", speed: undefined }
          : it,
      ),
    );
  }, []);

  const processQueue = useCallback(async () => {
    if (running) return;
    setRunning(true);
    try {
      // Drain queueRef sequentially. New items added during the run
      // are picked up because we re-read queueRef after each iteration.

      while (true) {
        const next = queueRef.current.shift();
        if (!next) break;
        await runOne(next);
      }
      onUploaded();
    } finally {
      setRunning(false);
    }
  }, [running, runOne, onUploaded]);

  const enqueue = useCallback(
    (file: File, relDir?: string) => {
      const id = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      const item: UploadItem = {
        id,
        file,
        relDir,
        status: "queued",
        progress: 0,
      };
      setItems((prev) => [...prev, item]);
      queueRef.current.push(item);
      onOpenRequest();
      void processQueue();
    },
    [processQueue, onOpenRequest],
  );

  useImperativeHandle(ref, () => ({ enqueue }), [enqueue]);

  const uploadProps: UploadProps = useMemo(
    () => ({
      name: "file",
      multiple: true,
      showUploadList: false,
      beforeUpload: (file) => {
        enqueue(file);
        return false; // we own the upload
      },
    }),
    [enqueue],
  );

  const totalProgress = useMemo(() => {
    if (items.length === 0) return 0;
    // A settled item (uploaded / failed / cancelled) counts as fully accounted
    // for, so a cancelled upload can't pin the batch bar below 100% forever.
    const settled = (s: UploadStatus) =>
      s === "success" || s === "error" || s === "cancelled";
    const sum = items.reduce(
      (acc, it) => acc + (settled(it.status) ? 1 : it.progress),
      0,
    );
    return sum / items.length;
  }, [items]);

  const completedCount = items.filter((it) => it.status === "success").length;
  const errorCount = items.filter((it) => it.status === "error").length;
  const cancelledCount = items.filter((it) => it.status === "cancelled").length;

  const clearCompleted = () => {
    setItems((prev) =>
      prev.filter(
        (it) =>
          it.status !== "success" &&
          it.status !== "error" &&
          it.status !== "cancelled",
      ),
    );
  };

  return (
    <>
    <Drawer
      title="Upload files"
      open={open}
      onClose={onClose}
      width={560}
      destroyOnHidden={false}
      extra={
        <Button type="text" icon={<CloseOutlined />} onClick={onClose} />
      }
      footer={
        items.length > 0 ? (
          <Space style={{ width: "100%", justifyContent: "space-between" }}>
            <Typography.Text type="secondary">
              {completedCount}/{items.length} done
              {errorCount > 0 ? ` · ${errorCount} failed` : ""}
              {cancelledCount > 0 ? ` · ${cancelledCount} cancelled` : ""}
            </Typography.Text>
            <Space>
              {(completedCount > 0 || errorCount > 0 || cancelledCount > 0) && (
                <Button onClick={clearCompleted} disabled={running}>
                  Clear completed
                </Button>
              )}
              <Button onClick={onClose}>Close</Button>
            </Space>
          </Space>
        ) : null
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Upload.Dragger {...uploadProps}>
          <p className="ant-upload-drag-icon">
            <InboxOutlined />
          </p>
          <p className="ant-upload-text">
            Click or drag files here to upload
          </p>
          <p className="ant-upload-hint">
            Multiple files supported.{" "}
            {isDirectHost
              ? `Direct upload, up to ${formatBytes(maxBytes)}.`
              : `Files over ${formatBytes(SINGLE_MULTIPART_CEILING)} use chunked upload with resume on disconnect.`}
          </p>
        </Upload.Dragger>

        {items.length > 0 && (
          <Progress
            percent={Math.round(totalProgress * 100)}
            status={
              errorCount > 0 && !running
                ? "exception"
                : running
                  ? "active"
                  : "success"
            }
          />
        )}

        <List
          dataSource={items}
          locale={{ emptyText: " " }}
          renderItem={(it) => (
            <List.Item key={it.id}>
              <Space direction="vertical" size={4} style={{ width: "100%" }}>
                <Space style={{ width: "100%", justifyContent: "space-between" }}>
                  <Typography.Text strong ellipsis style={{ maxWidth: 360 }}>
                    {it.file.name}
                  </Typography.Text>
                  <Typography.Text type="secondary">
                    {formatBytes(it.file.size)}
                  </Typography.Text>
                </Space>
                {it.status === "queued" && (
                  <Space style={{ width: "100%", justifyContent: "space-between" }}>
                    <Typography.Text type="secondary">Queued</Typography.Text>
                    <Button size="small" type="link" danger onClick={() => cancelItem(it.id)}>
                      Cancel
                    </Button>
                  </Space>
                )}
                {it.status === "uploading" && (
                  <>
                    <Progress
                      percent={Math.round(it.progress * 100)}
                      size="small"
                      status="active"
                    />
                    <Space style={{ width: "100%", justifyContent: "space-between" }}>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {it.speed && it.speed > 0 ? `${formatBytes(it.speed)}/s` : " "}
                      </Typography.Text>
                      <Button size="small" type="link" danger onClick={() => cancelItem(it.id)}>
                        Cancel
                      </Button>
                    </Space>
                  </>
                )}
                {it.status === "success" && (
                  <Typography.Text type="success">
                    <CheckCircleFilled /> Uploaded
                  </Typography.Text>
                )}
                {it.status === "error" && (
                  <Typography.Text type="danger">
                    <CloseCircleFilled /> {it.errorMessage}
                  </Typography.Text>
                )}
                {it.status === "cancelled" && (
                  <Typography.Text type="secondary">Cancelled</Typography.Text>
                )}
              </Space>
            </List.Item>
          )}
        />
      </Space>
    </Drawer>
    <Modal
      title="File already exists"
      open={conflictItem !== null}
      onCancel={() => resolveConflict("cancel")}
      maskClosable={false}
      footer={[
        <Button key="cancel" onClick={() => resolveConflict("cancel")}>
          Cancel
        </Button>,
        <Button key="keep" onClick={() => resolveConflict("keepboth")}>
          Keep both
        </Button>,
        <Button
          key="overwrite"
          type="primary"
          danger
          onClick={() => resolveConflict("overwrite")}
        >
          Overwrite
        </Button>,
      ]}
    >
      <Typography.Paragraph>
        <Typography.Text code>{conflictItem?.file.name}</Typography.Text>{" "}
        already exists in this folder.
      </Typography.Paragraph>
      <Checkbox
        checked={conflictAlways}
        onChange={(e) => setConflictAlways(e.target.checked)}
      >
        Always overwrite for the rest of this upload
      </Checkbox>
    </Modal>
    </>
  );
});

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
import { tenantFilesApi, type FilesApi } from "./filesApi";
import type { UploadOpts } from "./filesApi";

export interface UploadDrawerHandle {
  /** Enqueue a file for upload and open the drawer if closed. */
  enqueue: (file: File) => void;
}

type UploadStatus = "queued" | "uploading" | "success" | "error";

interface UploadItem {
  id: string;
  file: File;
  status: UploadStatus;
  progress: number;
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

const SINGLE_MULTIPART_CEILING = 100 * 1024 * 1024;
const CHUNK_SIZE = 10 * 1024 * 1024;
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
      try {
        if (item.file.size > maxBytes) {
          throw new Error(`exceeds the ${formatBytes(maxBytes)} upload limit`);
        }
        const doUpload = (opts?: UploadOpts) => {
          const onp = (frac: number) => updateItem(item.id, { progress: frac });
          return item.file.size <= SINGLE_MULTIPART_CEILING
            ? api.upload(currentPath, item.file, onp, opts)
            : api.uploadChunked(currentPath, item.file, CHUNK_SIZE, onp, opts);
        };
        let mode: "ask" | "overwrite" | "rename" = alwaysOverwriteRef.current
          ? "overwrite"
          : "ask";
        let renameN = 0;
        for (let guard = 0; guard < 1002; guard++) {
          let opts: UploadOpts | undefined;
          if (mode === "overwrite") opts = { overwrite: true };
          else if (mode === "rename")
            opts = { name: makeRenamedName(item.file.name, ++renameN) };
          updateItem(item.id, { status: "uploading", progress: 0 });
          try {
            await doUpload(opts);
            updateItem(item.id, { status: "success", progress: 1 });
            return;
          } catch (err) {
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
        updateItem(item.id, {
          status: "error",
          errorMessage: errMessage(err),
        });
      }
    },
    [currentPath, updateItem, askConflict, maxBytes],
  );

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
    (file: File) => {
      const id = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      const item: UploadItem = {
        id,
        file,
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
    const sum = items.reduce(
      (acc, it) => acc + (it.status === "success" ? 1 : it.progress),
      0,
    );
    return sum / items.length;
  }, [items]);

  const completedCount = items.filter((it) => it.status === "success").length;
  const errorCount = items.filter((it) => it.status === "error").length;

  const clearCompleted = () => {
    setItems((prev) =>
      prev.filter((it) => it.status !== "success" && it.status !== "error"),
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
            </Typography.Text>
            <Space>
              {(completedCount > 0 || errorCount > 0) && (
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
            Multiple files supported. Files &gt; 100 MB use chunked upload
            with resume on disconnect.
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
                  <Typography.Text type="secondary">Queued</Typography.Text>
                )}
                {it.status === "uploading" && (
                  <Progress
                    percent={Math.round(it.progress * 100)}
                    size="small"
                    status="active"
                  />
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

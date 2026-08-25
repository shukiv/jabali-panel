// RestoreProgressModal — upload + restore feedback for "Restore from file"
// (GH #1044). The reporter asked for a File-Manager-style upload experience on
// the per-database restore: progress, uploaded/total, speed, ETA, and a clear
// success/failure state, instead of clicking Restore and waiting blind.
//
// Two phases the modal makes visible:
//   1. uploading  — the browser streams the dump to the panel. Progress, bytes,
//                   speed and ETA come from axios's upload events (real numbers).
//   2. restoring  — the body is up; the server loads the dump synchronously and
//                   emits no further progress, so this leg is intentionally
//                   indeterminate ("Restoring…"). Large PostgreSQL dumps can sit
//                   here for minutes — that is expected, not a hang.
// Then a terminal success / error state the user dismisses.
//
// Presentational + controlled: the parent (UserDatabaseList) owns the progress
// object and drives phase transitions from the upload callback and the request
// outcome. Close is offered only in a terminal phase — the restore keeps running
// server-side regardless of this dialog (the request is detached from the tab),
// so there is nothing safe to cancel mid-flight.
import { Button, Modal, Progress, Space, Typography } from "antd";
import { CheckCircleOutlined, CloseCircleOutlined, LoadingOutlined } from "@icons";

export type RestorePhase = "uploading" | "restoring" | "success" | "error";

export interface RestoreProgress {
  dbName: string;
  fileName: string;
  fileSize: number;
  phase: RestorePhase;
  /** Bytes uploaded so far (upload leg only). */
  loaded: number;
  /** Upload speed in bytes/sec, when the transport reports it. */
  rate?: number;
  /** Estimated seconds remaining for the upload, when reported. */
  estimated?: number;
  errorMessage?: string;
}

interface RestoreProgressModalProps {
  progress: RestoreProgress | null;
  onClose: () => void;
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function formatRate(bytesPerSec?: number): string | null {
  if (!bytesPerSec || bytesPerSec <= 0) return null;
  return `${formatBytes(bytesPerSec)}/s`;
}

function formatDuration(seconds?: number): string | null {
  if (seconds === undefined || !isFinite(seconds) || seconds < 0) return null;
  const s = Math.round(seconds);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const rem = s % 60;
  return `${m}m ${rem}s`;
}

export function RestoreProgressModal({ progress, onClose }: RestoreProgressModalProps) {
  const p = progress;
  const active = p?.phase === "uploading" || p?.phase === "restoring";

  const percent =
    p && p.fileSize > 0 ? Math.min(100, Math.round((p.loaded / p.fileSize) * 100)) : 0;

  const rate = formatRate(p?.rate);
  const eta = formatDuration(p?.estimated);

  return (
    <Modal
      open={p !== null}
      title={p ? `Restore ${p.dbName}` : "Restore"}
      maskClosable={!active}
      closable={!active}
      keyboard={!active}
      onCancel={active ? undefined : onClose}
      footer={
        active ? null : (
          <Button type="primary" onClick={onClose}>
            Close
          </Button>
        )
      }
    >
      {p && (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Space style={{ width: "100%", justifyContent: "space-between" }}>
            <Typography.Text strong ellipsis style={{ maxWidth: 340 }}>
              {p.fileName}
            </Typography.Text>
            <Typography.Text type="secondary">{formatBytes(p.fileSize)}</Typography.Text>
          </Space>

          {p.phase === "uploading" && (
            <>
              <Progress percent={percent} status="active" />
              <Space
                style={{ width: "100%", justifyContent: "space-between" }}
                size="large"
                wrap
              >
                <Typography.Text type="secondary">
                  Uploading — {formatBytes(p.loaded)} / {formatBytes(p.fileSize)}
                </Typography.Text>
                <Typography.Text type="secondary">
                  {[rate, eta ? `~${eta} left` : null].filter(Boolean).join(" · ") || " "}
                </Typography.Text>
              </Space>
            </>
          )}

          {p.phase === "restoring" && (
            <>
              <Progress percent={100} status="success" showInfo={false} />
              <Space>
                <LoadingOutlined />
                <Typography.Text>
                  Upload complete — restoring database…
                </Typography.Text>
              </Space>
              <Typography.Text type="secondary">
                A large dump can take several minutes. You can leave this open;
                the restore continues on the server even if you close it.
              </Typography.Text>
            </>
          )}

          {p.phase === "success" && (
            <Typography.Text type="success">
              <CheckCircleOutlined /> Database “{p.dbName}” restored from{" "}
              {p.fileName}.
            </Typography.Text>
          )}

          {p.phase === "error" && (
            <Typography.Text type="danger">
              <CloseCircleOutlined /> Restore failed
              {p.errorMessage ? `: ${p.errorMessage}` : "."}
            </Typography.Text>
          )}
        </Space>
      )}
    </Modal>
  );
}

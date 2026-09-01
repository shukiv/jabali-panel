// RestoreFullServerDrawer — GH #1408 phase 2. Restore from an uploaded FULL
// SERVER container: upload the one-file archive produced by "Package & download",
// pick which users to restore, and each is restored exactly like a normal
// account restore. System restore is intentionally left to the CLI.
import { useState } from "react";
import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Progress,
  Space,
  Typography,
  Upload,
} from "antd";
import { InboxOutlined } from "@icons";
import { feedback } from "../../../lib/feedback";
import {
  applyFullServerRestore,
  inspectFullServerContainer,
  uploadBackupArchiveChunked,
  type FullContainerInfo,
  type FullRestoreResult,
} from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

interface Props {
  open: boolean;
  onClose: () => void;
}

type Phase = "pick" | "uploading" | "inspecting" | "ready" | "applying" | "done";

export function RestoreFullServerDrawer({ open, onClose }: Props) {
  const [file, setFile] = useState<File | null>(null);
  const [phase, setPhase] = useState<Phase>("pick");
  const [pct, setPct] = useState(0);
  const [info, setInfo] = useState<FullContainerInfo | null>(null);
  const [uploadId, setUploadId] = useState<string | null>(null);
  const [users, setUsers] = useState<string[]>([]);
  const [result, setResult] = useState<FullRestoreResult | null>(null);

  const reset = () => {
    setFile(null);
    setPhase("pick");
    setPct(0);
    setInfo(null);
    setUploadId(null);
    setUsers([]);
    setResult(null);
  };
  const close = () => {
    reset();
    onClose();
  };

  const startUpload = async () => {
    if (!file) return;
    setPhase("uploading");
    setPct(0);
    try {
      const id = await uploadBackupArchiveChunked(file, (p) => setPct(Math.round(p.frac * 100)));
      setUploadId(id);
      setPhase("inspecting");
      const meta = await inspectFullServerContainer(id);
      setInfo(meta);
      setUsers(meta.users); // default: restore all users in the container
      setPhase("ready");
    } catch (err) {
      feedback.message.error(extractApiError(err, "Upload / inspect failed"));
      setPhase("pick");
    }
  };

  const apply = async () => {
    if (!uploadId || users.length === 0) return;
    setPhase("applying");
    feedback.message.info("Restore started — running in the background");
    try {
      const r = await applyFullServerRestore(uploadId, users, false);
      setResult(r);
      setPhase("done");
      feedback.message.success("Full server restore finished — see details");
    } catch (err) {
      feedback.message.error(extractApiError(err, "Restore failed"));
      setPhase("ready");
    }
  };

  return (
    <Drawer
      title="Restore full server from upload"
      width={520}
      open={open}
      onClose={close}
      destroyOnClose
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Upload a Full Server backup archive (from “Package &amp; download”) and
          restore the accounts it contains. Each user is restored just like a
          normal account restore. Create any missing users first.
        </Typography.Paragraph>

        {(phase === "pick" || phase === "uploading") && (
          <>
            <Upload.Dragger
              multiple={false}
              maxCount={1}
              accept=".tar,.zst,.tar.zst"
              beforeUpload={(f) => {
                setFile(f);
                return false;
              }}
              onRemove={() => setFile(null)}
              disabled={phase === "uploading"}
            >
              <p className="ant-upload-drag-icon">
                <InboxOutlined />
              </p>
              <p className="ant-upload-text">Click or drag the full-server .tar here</p>
            </Upload.Dragger>
            {phase === "uploading" && <Progress percent={pct} status="active" />}
            <Button type="primary" disabled={!file || phase === "uploading"} loading={phase === "uploading"} onClick={startUpload}>
              Upload &amp; inspect
            </Button>
          </>
        )}

        {phase === "inspecting" && <Progress percent={100} status="active" />}

        {info && (phase === "ready" || phase === "applying" || phase === "done") && (
          <>
            <Alert
              type="success"
              showIcon
              message={`Full server backup — ${info.users.length} user(s)`}
              description={info.has_system ? "Includes a system backup (restored via the CLI, not here)." : undefined}
            />
            <div>
              <Typography.Text strong>Restore these users</Typography.Text>
              <Checkbox.Group
                value={users}
                onChange={(v) => setUsers(v as string[])}
                style={{ display: "flex", flexDirection: "column", gap: 8, marginTop: 4 }}
                options={info.users.map((u) => ({ label: u, value: u }))}
              />
            </div>
            <Alert
              type="warning"
              showIcon
              message="This overwrites the selected users' data"
              description="Each selected account's home directory and databases are replaced with the backup. This cannot be undone. Restore into users with the same names as in the backup."
            />
            {phase === "applying" && (
              <Alert
                type="info"
                showIcon
                message="Restoring in the background"
                description="Restoring each account can take a while. Keep this drawer open to see the result."
              />
            )}
            {phase !== "done" && (
              <Button
                type="primary"
                danger
                loading={phase === "applying"}
                disabled={phase === "applying" || users.length === 0}
                onClick={apply}
              >
                {phase === "applying" ? "Restoring…" : `Restore ${users.length} user(s)`}
              </Button>
            )}
          </>
        )}

        {result && phase === "done" && (
          <Alert
            type="success"
            showIcon
            message="Restore result"
            description={
              <Space direction="vertical" size={2}>
                {(result.packed ?? []).map((p, i) => (
                  <span key={i}>{p}</span>
                ))}
                {(result.skipped ?? []).length > 0 && (
                  <Typography.Text type="secondary">
                    Skipped: {result.skipped?.join(", ")}
                  </Typography.Text>
                )}
                <Button size="small" onClick={reset} style={{ marginTop: 8 }}>
                  Restore another
                </Button>
              </Space>
            }
          />
        )}
      </Space>
    </Drawer>
  );
}

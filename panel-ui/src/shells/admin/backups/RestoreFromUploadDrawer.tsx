// RestoreFromUploadDrawer — GH #1408. Admin restore from an uploaded backup
// archive (DR / cross-server migration): upload the .tar downloaded earlier,
// inspect it, pick components + a target user, and restore. The apply is
// step-up gated server-side; a stepup_required response bounces to re-auth.
import { useState } from "react";
import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Input,
  Progress,
  Space,
  Typography,
  Upload,
} from "antd";
import { InboxOutlined } from "@icons";
import { feedback } from "../../../lib/feedback";
import {
  applyUploadedBackupRestore,
  inspectUploadedBackup,
  stepUpRedirect,
  uploadBackupArchiveChunked,
  type UploadedBackupInfo,
  type UploadedBackupRestoreResult,
} from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

const COMPONENT_LABELS: Record<string, string> = {
  home: "Home directory (website files)",
  db: "Databases",
  mail: "Mail (staged for manual apply)",
  dns: "DNS records",
  docker: "Docker apps (restored stopped)",
};

interface Props {
  open: boolean;
  onClose: () => void;
}

type Phase = "pick" | "uploading" | "inspecting" | "ready" | "applying" | "done";

export function RestoreFromUploadDrawer({ open, onClose }: Props) {
  const [file, setFile] = useState<File | null>(null);
  const [phase, setPhase] = useState<Phase>("pick");
  const [pct, setPct] = useState(0);
  const [info, setInfo] = useState<UploadedBackupInfo | null>(null);
  const [uploadId, setUploadId] = useState<string | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [targetUser, setTargetUser] = useState("");
  const [result, setResult] = useState<UploadedBackupRestoreResult | null>(null);

  const reset = () => {
    setFile(null);
    setPhase("pick");
    setPct(0);
    setInfo(null);
    setUploadId(null);
    setSelected([]);
    setTargetUser("");
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
      const id = await uploadBackupArchiveChunked(file, (p) =>
        setPct(Math.round(p.frac * 100)),
      );
      setUploadId(id);
      setPhase("inspecting");
      const meta = await inspectUploadedBackup(id);
      setInfo(meta);
      setSelected(meta.components); // default: everything the archive holds
      setTargetUser(meta.user.username); // v1: restore into the same username
      setPhase("ready");
    } catch (err) {
      feedback.message.error(extractApiError(err, "Upload / inspect failed"));
      setPhase("pick");
    }
  };

  const apply = async () => {
    if (!uploadId || !targetUser || selected.length === 0) return;
    setPhase("applying");
    try {
      const r = await applyUploadedBackupRestore(uploadId, targetUser, selected);
      setResult(r);
      setPhase("done");
      const n = r.applied?.length ?? 0;
      if (n > 0) feedback.message.success(`Restored ${n} item(s) into ${targetUser}`);
      else feedback.message.warning("Nothing was applied — see details");
    } catch (err) {
      // Step-up: the server demands recent auth for this destructive action.
      const e = err as { response?: { status?: number; data?: { error?: string } } };
      if (e?.response?.status === 401 && e.response.data?.error === "stepup_required") {
        feedback.message.info("Please re-authenticate to run a restore");
        stepUpRedirect();
        return;
      }
      feedback.message.error(extractApiError(err, "Restore failed"));
      setPhase("ready");
    }
  };

  return (
    <Drawer
      title="Restore from uploaded backup"
      width={520}
      open={open}
      onClose={close}
      destroyOnClose
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Upload a backup archive you downloaded earlier (the per-account{" "}
          <code>.tar</code>) and restore it into an existing user — useful for
          disaster recovery or moving a user to a new server. Create the target
          user first if it doesn&apos;t exist yet.
        </Typography.Paragraph>

        <Alert
          type="info"
          showIcon
          message="Large archives: upload directly to the server"
          description="For very large backups, open the panel by the server's IP address rather than through a proxied domain (e.g. Cloudflare) to avoid upload-size limits."
        />

        {(phase === "pick" || phase === "uploading") && (
          <>
            <Upload.Dragger
              multiple={false}
              maxCount={1}
              accept=".tar,.zst,.tar.zst"
              beforeUpload={(f) => {
                setFile(f);
                return false; // don't auto-upload; we drive the chunked upload
              }}
              onRemove={() => setFile(null)}
              disabled={phase === "uploading"}
            >
              <p className="ant-upload-drag-icon">
                <InboxOutlined />
              </p>
              <p className="ant-upload-text">Click or drag the backup .tar here</p>
            </Upload.Dragger>
            {phase === "uploading" && <Progress percent={pct} status="active" />}
            <Button
              type="primary"
              disabled={!file || phase === "uploading"}
              loading={phase === "uploading"}
              onClick={startUpload}
            >
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
              message={`Backup of ${info.user.username}`}
              description={info.user.email || undefined}
            />
            <div>
              <Typography.Text strong>Restore into user</Typography.Text>
              <Input
                value={targetUser}
                onChange={(e) => setTargetUser(e.target.value.trim())}
                placeholder="existing username"
                style={{ marginTop: 4 }}
                disabled={phase !== "ready"}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Must be an existing user. Restore into the same username the
                backup came from.
              </Typography.Text>
            </div>
            <div>
              <Typography.Text strong>Components</Typography.Text>
              <Checkbox.Group
                value={selected}
                onChange={(v) => setSelected(v as string[])}
                style={{ display: "flex", flexDirection: "column", gap: 8, marginTop: 4 }}
                options={info.components.map((c) => ({
                  label: COMPONENT_LABELS[c] ?? c,
                  value: c,
                }))}
              />
            </div>
            <Alert
              type="warning"
              showIcon
              message="This overwrites the target user's selected data"
              description="Restoring the home directory and databases replaces the live contents with the backup. This cannot be undone."
            />
            {phase !== "done" && (
              <Button
                type="primary"
                danger
                loading={phase === "applying"}
                disabled={!targetUser || selected.length === 0}
                onClick={apply}
              >
                Restore into {targetUser || "user"}
              </Button>
            )}
          </>
        )}

        {result && phase === "done" && (
          <Alert
            type={(result.applied?.length ?? 0) > 0 ? "success" : "info"}
            showIcon
            message="Restore result"
            description={
              <Space direction="vertical" size={2}>
                {(result.applied ?? []).map((a) => (
                  <span key={a}>✓ {a}</span>
                ))}
                {(result.warnings ?? []).map((w, i) => (
                  <Typography.Text type="secondary" key={i}>
                    {w}
                  </Typography.Text>
                ))}
                {(result.metadata_errors ?? []).map((m, i) => (
                  <Typography.Text type="danger" key={`m${i}`}>
                    {m}
                  </Typography.Text>
                ))}
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

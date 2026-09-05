// RestoreFullServerDrawer — GH #1408 phase 2. Restore from an uploaded FULL
// SERVER container: upload the one-file archive produced by "Package & download",
// pick which users to restore, and each is restored exactly like a normal
// account restore. System restore is intentionally left to the CLI.
import { useMemo, useState } from "react";
import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Progress,
  Select,
  Space,
  Typography,
  Upload,
} from "antd";
import { InboxOutlined } from "@icons";
import { feedback } from "../../../lib/feedback";
import { useSelectQuery } from "../../../hooks/useSelectQuery";
import {
  applyFullServerRestore,
  inspectFullServerContainer,
  uploadBackupArchiveChunked,
  type FullContainerInfo,
  type FullRestoreResult,
} from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

type HostingPackage = { id: string; name: string };

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
  const [createMissing, setCreateMissing] = useState(false);
  const [packageId, setPackageId] = useState<string | null>(null);
  const [result, setResult] = useState<FullRestoreResult | null>(null);

  // Which container users don't yet exist on this server (from inspect). Older
  // panels omit user_status → treat existence as unknown (no create UI).
  const missingSet = useMemo(() => {
    const s = new Set<string>();
    for (const st of info?.user_status ?? []) {
      if (!st.exists) s.add(st.username);
    }
    return s;
  }, [info]);
  // Missing users the admin actually selected — the ones that need creating.
  const selectedMissing = users.filter((u) => missingSet.has(u));
  const canCreate = (info?.create_supported ?? false) && selectedMissing.length > 0;

  const reset = () => {
    setFile(null);
    setPhase("pick");
    setPct(0);
    setInfo(null);
    setUploadId(null);
    setUsers([]);
    setCreateMissing(false);
    setPackageId(null);
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
      const r = await applyFullServerRestore(uploadId, users, false, {
        createMissing: canCreate && createMissing,
        packageId: canCreate && createMissing ? packageId : null,
      });
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
          normal account restore. Accounts that don&apos;t exist yet can be
          created for you.
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
                options={info.users.map((u) => ({
                  label: missingSet.has(u) ? (
                    <span>
                      {u}{" "}
                      <Typography.Text type="warning" style={{ fontSize: 12 }}>
                        (not on this server)
                      </Typography.Text>
                    </span>
                  ) : (
                    u
                  ),
                  value: u,
                }))}
              />
            </div>

            {canCreate && (
              <div>
                <Checkbox
                  checked={createMissing}
                  onChange={(e) => setCreateMissing(e.target.checked)}
                  disabled={phase !== "ready"}
                >
                  Create the {selectedMissing.length} missing account(s):{" "}
                  <Typography.Text code>{selectedMissing.join(", ")}</Typography.Text>
                </Checkbox>
                {createMissing && (
                  <div style={{ marginTop: 8 }}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      Hosting package for the new accounts
                    </Typography.Text>
                    <PackageSelect value={packageId} onChange={setPackageId} disabled={phase !== "ready"} />
                    <Typography.Text type="secondary" style={{ fontSize: 12, display: "block", marginTop: 4 }}>
                      New accounts get a random password (set one afterwards) and
                      the identity/limits from the backup are never trusted —
                      admin accounts in the backup are refused.
                    </Typography.Text>
                  </div>
                )}
              </div>
            )}
            {selectedMissing.length > 0 && !createMissing && (
              <Alert
                type="warning"
                showIcon
                message="Some selected accounts don't exist yet"
                description={
                  info.create_supported
                    ? "Tick “Create the missing account(s)” above, or create them first — otherwise they're skipped."
                    : "Create them first — otherwise they're skipped."
                }
              />
            )}

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

function PackageSelect(props: {
  value: string | null;
  onChange: (v: string | null) => void;
  disabled?: boolean;
}) {
  const { options, isLoading } = useSelectQuery<HostingPackage>({
    resource: "packages",
    labelField: "name",
    valueField: "id",
  });
  return (
    <Select
      placeholder="Select a package (optional)"
      allowClear
      style={{ width: "100%", marginTop: 4 }}
      loading={isLoading}
      disabled={props.disabled}
      options={[{ label: "No package", value: null }, ...options]}
      value={props.value}
      onChange={(v: string | null) => props.onChange(v ?? null)}
    />
  );
}

// RestoreFromUploadDrawer — GH #1408. Admin restore from an uploaded backup
// archive (DR / cross-server migration): upload the .tar downloaded earlier,
// inspect it, pick components + a target user, and restore. The apply is
// admin-only. The restore runs in the background (202 + status poll), so the
// drawer shows a clear "running in the background" state until it seals.
//
// GH #1993: in admin mode the upload is kept on the server with a retention
// choice and listed under Backups, so a failed restore is retried without
// uploading again. Opened with `uploaded`, the drawer restores a kept upload.
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Input,
  Progress,
  Radio,
  Select,
  Space,
  Typography,
  Upload,
} from "antd";
import { InboxOutlined } from "@icons";
import { feedback } from "../../../lib/feedback";
import {
  applyUploadedBackupRestore,
  getUploadedBackup,
  inspectUploadedBackup,
  registerUploadedBackup,
  restoreKeptUploadedBackup,
  uploadBackupArchiveChunked,
  type UploadedBackup,
  type UploadedBackupInfo,
  type UploadedBackupRestoreResult,
  type UploadedBackupRetention,
} from "../../../apiClient";
import { useListQuery } from "../../../hooks/useQueries";
import { extractApiError } from "../../../apiErrors";

const COMPONENT_LABELS: Record<string, string> = {
  home: "Home directory (website files)",
  db: "Databases",
  mail: "Mail (mailboxes and messages)",
  dns: "DNS records",
  docker: "Docker apps (restored stopped)",
};

interface Props {
  open: boolean;
  onClose: () => void;
  // GH #1408: ownerMode drives the tenant self-service restore — target is the
  // caller (no username field), the API is /me/backups, and only the audited
  // components (files/db/mail) are offered.
  ownerMode?: boolean;
  // GH #1993 (admin): restore this kept upload instead of uploading a file.
  uploaded?: UploadedBackup | null;
  // GH #1993 (admin): called when a kept upload was added or restored, so the
  // Backups list refreshes.
  onKeptChange?: () => void;
}

const RETENTION_OPTIONS: { value: UploadedBackupRetention; label: string }[] = [
  { value: "keep", label: "Keep it on this server until I delete it" },
  { value: "keep_7_days", label: "Keep it for 7 days" },
  { value: "delete_after_restore", label: "Delete it once a restore succeeds" },
];

type Phase = "pick" | "uploading" | "inspecting" | "ready" | "applying" | "done";

const OWNER_COMPONENTS = ["home", "db", "mail"];

export function RestoreFromUploadDrawer({ open, onClose, ownerMode, uploaded, onKeptChange }: Props) {
  const base = ownerMode ? "/me/backups" : "/admin/backups";
  // GH #1993: admin uploads are kept on the server; the restore runs from there.
  const keepUploads = !ownerMode;
  const [retention, setRetention] = useState<UploadedBackupRetention>("keep");
  const [kept, setKept] = useState<UploadedBackup | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [phase, setPhase] = useState<Phase>("pick");
  const [pct, setPct] = useState(0);
  const [info, setInfo] = useState<UploadedBackupInfo | null>(null);
  const [uploadId, setUploadId] = useState<string | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [targetUser, setTargetUser] = useState("");
  const [result, setResult] = useState<UploadedBackupRestoreResult | null>(null);
  // GH #1408 create-from-manifest: when the bundle's user isn't on this box yet.
  const [createUser, setCreateUser] = useState(false);
  const [packageId, setPackageId] = useState<string | null>(null);
  const { items: packages } = useListQuery<{ id: string; name: string }>({ resource: "packages" });

  const reset = () => {
    setFile(null);
    setPhase("pick");
    setPct(0);
    setInfo(null);
    setUploadId(null);
    setSelected([]);
    setTargetUser("");
    setResult(null);
    setCreateUser(false);
    setPackageId(null);
    setRetention("keep");
    setKept(null);
  };

  // showKept fills the ready phase from a kept upload.
  const showKept = (b: UploadedBackup) => {
    setKept(b);
    setInfo({
      user: { id: "", username: b.account_username, email: b.account_email || undefined },
      components: b.components,
      target_exists: b.target_exists,
      create_supported: b.create_supported,
    });
    setSelected(b.components);
    setTargetUser(b.account_username);
    setCreateUser(b.target_exists === false && b.create_supported);
    setPhase("ready");
  };

  const uploadedId = uploaded?.id;
  useEffect(() => {
    if (!open || !uploadedId) return;
    let cancelled = false;
    setPhase("inspecting");
    getUploadedBackup(uploadedId)
      .then((b) => {
        if (!cancelled) showKept(b);
      })
      .catch((err) => {
        if (cancelled) return;
        feedback.message.error(extractApiError(err, "Could not load the uploaded backup"));
        onClose();
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, uploadedId]);

  const close = () => {
    reset();
    onClose();
  };

  const startUpload = async () => {
    if (!file) return;
    setPhase("uploading");
    setPct(0);
    try {
      const id = await uploadBackupArchiveChunked(
        file,
        (p) => setPct(Math.round(p.frac * 100)),
        base,
      );
      setUploadId(id);
      setPhase("inspecting");
      if (keepUploads) {
        // The server checks the archive while keeping it.
        const b = await registerUploadedBackup(id, retention, file.name);
        onKeptChange?.();
        showKept(b);
        return;
      }
      const meta = await inspectUploadedBackup(id, base);
      setInfo(meta);
      // Default selection = everything the archive holds, restricted to the
      // audited-safe set in ownerMode (docker/dns aren't self-service).
      const offered = ownerMode
        ? meta.components.filter((c) => OWNER_COMPONENTS.includes(c))
        : meta.components;
      setSelected(offered);
      setTargetUser(meta.user.username); // restore into the same username (home is name-keyed)
      // GH #1408: offer create-from-backup when the bundle's user isn't here yet.
      setCreateUser(!ownerMode && meta.target_exists === false && meta.create_supported === true);
      setPhase("ready");
    } catch (err) {
      feedback.message.error(extractApiError(err, "Upload / inspect failed"));
      setPhase("pick");
    }
  };

  const apply = async () => {
    if ((!uploadId && !kept) || !targetUser || selected.length === 0) return;
    setPhase("applying");
    // The restore is accepted immediately and runs in the background; the call
    // below polls its status until it seals. Tell the admin it's running so an
    // empty "applying" state doesn't look like nothing happened (GH #1408).
    feedback.message.info("Restore started — running in the background");
    const opts = createUser ? { createUser: true, packageId } : undefined;
    try {
      const r = kept
        ? await restoreKeptUploadedBackup(kept.id, targetUser, selected, opts)
        : await applyUploadedBackupRestore(uploadId as string, targetUser, selected, base, opts);
      if (kept) onKeptChange?.();
      setResult(r);
      setPhase("done");
      const n = r.applied?.length ?? 0;
      if (n > 0) feedback.message.success(`Restored ${n} item(s) into ${targetUser}`);
      else feedback.message.warning("Nothing was applied — see details");
    } catch (err) {
      const reason = extractApiError(err, "Restore failed");
      if (kept) {
        onKeptChange?.();
        feedback.message.error(`${reason} — the uploaded backup is kept; restore it again from Backups`);
      } else {
        feedback.message.error(reason);
      }
      setPhase("ready");
    }
  };

  return (
    <Drawer
      title={uploaded ? "Restore uploaded backup" : "Restore from uploaded backup"}
      width={520}
      open={open}
      onClose={close}
      destroyOnClose
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          {uploaded ? (
            <>
              Restore this uploaded backup into an account. It stays on this
              server afterwards, unless it was uploaded to be deleted once a
              restore succeeds.
            </>
          ) : ownerMode ? (
            <>
              Upload a backup archive you downloaded earlier (the{" "}
              <code>.tar</code>) and restore it into <strong>your own account</strong>{" "}
              — files, databases, and mail. Databases and mail domains you no
              longer own are skipped; recreate a database first if you need it back.
            </>
          ) : (
            <>
              Upload a backup archive you downloaded earlier (the per-account{" "}
              <code>.tar</code>) and restore it — useful for disaster recovery or
              moving a user to a new server. If the user doesn&apos;t exist yet,
              the panel can create it from the backup. The upload is listed under
              Backups, so a failed restore can be retried without uploading again.
            </>
          )}
        </Typography.Paragraph>

        {/* GH #1408: the "upload via the server's direct IP" notice was removed —
            direct-IP panel access/login is currently broken, so the advice was
            misleading. Restore it once direct-IP access works again. */}

        {!uploaded && (phase === "pick" || phase === "uploading") && (
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
            {keepUploads && (
              <div>
                <Typography.Text strong>After the restore</Typography.Text>
                <Radio.Group
                  value={retention}
                  onChange={(e) => setRetention(e.target.value as UploadedBackupRetention)}
                  disabled={phase === "uploading"}
                  style={{ display: "flex", flexDirection: "column", gap: 4, marginTop: 4 }}
                  options={RETENTION_OPTIONS}
                />
              </div>
            )}
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
            {!ownerMode && info.target_exists === false ? (
              info.create_supported ? (
                // GH #1408 create-from-manifest: the bundle's user isn't here yet.
                <div>
                  <Alert
                    type="info"
                    showIcon
                    message={`User "${info.user.username}" doesn't exist yet — it will be created from this backup`}
                    description="A new non-admin account is created with this username and email, then the backup is restored into it. Its password is regenerated (not restored) — send the user a recovery link after the restore."
                  />
                  <div style={{ marginTop: 8 }}>
                    <Typography.Text strong>Package</Typography.Text>
                    <Select
                      allowClear
                      value={packageId ?? undefined}
                      onChange={(v) => setPackageId(v ?? null)}
                      placeholder="No package (unrestricted limits)"
                      style={{ width: "100%", marginTop: 4 }}
                      disabled={phase !== "ready"}
                      options={(packages ?? []).map((p) => ({ label: p.name, value: p.id }))}
                    />
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      No package = unrestricted resource limits. Pick a package to cap the new account.
                    </Typography.Text>
                  </div>
                </div>
              ) : (
                <Alert
                  type="warning"
                  showIcon
                  message={`User "${info.user.username}" doesn't exist`}
                  description="This server can't create it from the backup — create the user manually first, then re-run the restore."
                />
              )
            ) : !ownerMode ? (
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
                  Restore into the same username the backup came from.
                </Typography.Text>
              </div>
            ) : null}
            <div>
              <Typography.Text strong>Components</Typography.Text>
              <Checkbox.Group
                value={selected}
                onChange={(v) => setSelected(v as string[])}
                style={{ display: "flex", flexDirection: "column", gap: 8, marginTop: 4 }}
                options={(ownerMode
                  ? info.components.filter((c) => OWNER_COMPONENTS.includes(c))
                  : info.components
                ).map((c) => ({
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
            {phase === "applying" && (
              <Alert
                type="info"
                showIcon
                message={`Restoring ${info.user.username} in the background`}
                description="This can take several minutes for large backups. Keep this drawer open to see the result, or come back later — the restore keeps running on the server."
              />
            )}
            {phase !== "done" && (
              <Button
                type="primary"
                danger
                loading={phase === "applying"}
                disabled={
                  phase === "applying" ||
                  !targetUser ||
                  selected.length === 0 ||
                  // create needed but this server can't create the user
                  (!ownerMode && info.target_exists === false && !info.create_supported)
                }
                onClick={apply}
              >
                {phase === "applying"
                  ? "Restoring…"
                  : ownerMode
                    ? "Restore into my account"
                    : createUser
                      ? `Create ${targetUser} & restore`
                      : `Restore into ${targetUser || "user"}`}
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
                {kept && kept.retention !== "delete_after_restore" && (
                  <span>The uploaded backup stays listed under Backups.</span>
                )}
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
                {!uploaded && (
                  <Button size="small" onClick={reset} style={{ marginTop: 8 }}>
                    Restore another
                  </Button>
                )}
              </Space>
            }
          />
        )}
      </Space>
    </Drawer>
  );
}

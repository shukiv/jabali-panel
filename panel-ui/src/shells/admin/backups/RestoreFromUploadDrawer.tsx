// RestoreFromUploadDrawer — GH #1408. Admin restore from an uploaded backup
// archive (DR / cross-server migration): upload the .tar downloaded earlier,
// inspect it, pick components + a target user, and restore. The apply is
// admin-only. The restore runs in the background (202 + status poll), so the
// drawer shows a clear "running in the background" state until it seals.
//
// GH #1993: in admin mode the upload is kept on the server with a retention
// choice and listed under Backups, so a failed restore is retried without
// uploading again. Opened with `uploaded`, the drawer restores a kept upload.
// A restore adds only what the account is missing unless "Overwrite existing
// items with the backup" is checked. Before Restore, the drawer shows the
// backup checked against this server (the preflight); a blocking check
// disables Restore.
import { useEffect, useState } from "react";
import axios from "axios";
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
  getUploadedBackupPreflight,
  inspectUploadedBackup,
  registerUploadedBackup,
  restoreKeptUploadedBackup,
  uploadBackupArchiveChunked,
  type RestorePreflight,
  type RestoreProgress,
  type UploadedBackup,
  type UploadedBackupInfo,
  type UploadedBackupRestoreResult,
  type UploadedBackupRetention,
} from "../../../apiClient";
import { useListQuery } from "../../../hooks/useQueries";
import { extractApiError } from "../../../apiErrors";
import { RestoreProgressView } from "./RestoreProgressView";
import { RestorePreflightView } from "./RestorePreflightView";

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
  // GH #1993: the running restore's step and what it is doing.
  const [progress, setProgress] = useState<RestoreProgress | null>(null);
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
  // GH #1993: off = keep what the account already has, add what is missing.
  const [overwrite, setOverwrite] = useState(false);
  // GH #1993: install the backup's SSL certificates (admin only; off by
  // default, JAB-54: a source's private key is not trusted unasked).
  const [keepCerts, setKeepCerts] = useState(false);
  // GH #1993: the backup checked against this server, and whether that check
  // is still running.
  const [preflight, setPreflight] = useState<RestorePreflight | null>(null);
  const [checking, setChecking] = useState(false);
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
    setProgress(null);
    setOverwrite(false);
    setKeepCerts(false);
    setPreflight(null);
    setChecking(false);
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
    setPreflight(b.preflight ?? null);
    setPhase("ready");
  };

  const uploadedId = uploaded?.id;
  useEffect(() => {
    if (!open || !uploadedId) return;
    let cancelled = false;
    setPhase("inspecting");
    getUploadedBackup(uploadedId)
      .then((b) => {
        if (cancelled) return;
        showKept(b);
        setChecking(true);
        return getUploadedBackupPreflight(uploadedId)
          .then((p) => {
            if (!cancelled) setPreflight(p);
          })
          .catch((err) => {
            if (!cancelled) feedback.message.error(extractApiError(err, "Could not check the backup against this server"));
          })
          .finally(() => {
            if (!cancelled) setChecking(false);
          });
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
      setPreflight(meta.preflight ?? null);
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
    setProgress(null);
    // The restore is accepted immediately and runs in the background; the call
    // below polls its status until it seals. Tell the admin it's running so an
    // empty "applying" state doesn't look like nothing happened (GH #1408).
    feedback.message.info("Restore started — running in the background");
    const opts = {
      ...(createUser ? { createUser: true, packageId } : {}),
      overwrite,
      ...(!ownerMode && keepCerts ? { keepCertificates: true } : {}),
    };
    try {
      const r = kept
        ? await restoreKeptUploadedBackup(kept.id, targetUser, selected, opts, setProgress)
        : await applyUploadedBackupRestore(uploadId as string, targetUser, selected, base, opts, setProgress);
      if (kept) onKeptChange?.();
      setResult(r);
      setPhase("done");
      const n = r.applied?.length ?? 0;
      if (n > 0) feedback.message.success(`Restored ${n} item(s) into ${targetUser}`);
      else feedback.message.warning("Nothing was applied — see details");
    } catch (err) {
      const reason = extractApiError(err, "Restore failed");
      // The server ran the preflight again and it blocked: show its checks.
      if (axios.isAxiosError(err) && err.response?.data?.error === "restore_preflight_blocked" && err.response.data.preflight) {
        setPreflight(err.response.data.preflight as RestorePreflight);
      }
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
            <RestorePreflightView preflight={preflight} checking={checking} />
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
            <Checkbox
              checked={overwrite}
              onChange={(e) => setOverwrite(e.target.checked)}
              disabled={phase !== "ready"}
            >
              Overwrite existing items with the backup
            </Checkbox>
            {overwrite ? (
              <Alert
                type="warning"
                showIcon
                message={
                  ownerMode
                    ? "This overwrites your account's selected data"
                    : "This overwrites the target user's selected data"
                }
                description={
                  ownerMode
                    ? "The home directory and databases are replaced with the backup's: files added since the backup are deleted. This cannot be undone."
                    : "The home directory and databases are replaced with the backup's: files added since the backup are deleted. Mailboxes and database users the account already has take the backup's settings; the restore report lists what is kept. This cannot be undone."
                }
              />
            ) : (
              <Alert
                type="info"
                showIcon
                message="Only what is missing is added"
                description={
                  ownerMode
                    ? "Files and databases already in your account stay as they are. A database that already has data is skipped, and the result lists what was kept. Mail already there is not copied twice."
                    : "Files, databases and Docker apps already in the account stay as they are, and so do its mailboxes' auto-replies. A database or Docker app that already has data is skipped, and the result lists what was kept. Mail already there is not copied twice."
                }
              />
            )}
            {!ownerMode && (
              <Space direction="vertical" size={4}>
                <Checkbox
                  checked={keepCerts}
                  onChange={(e) => setKeepCerts(e.target.checked)}
                  disabled={phase !== "ready"}
                >
                  Keep the backup&apos;s SSL certificates
                </Checkbox>
                <Typography.Text type="secondary">
                  {keepCerts
                    ? "Each domain's certificate and private key from the backup is installed when it covers the domain, is signed by a trusted certificate authority and is still valid. Let's Encrypt takes over before it expires, once the domain's DNS points to this server."
                    : "Let's Encrypt issues new certificates once the domains' DNS points to this server. Choose this only for a backup you trust: it installs the private keys it carries."}
                </Typography.Text>
              </Space>
            )}
            {phase === "applying" && (
              <Alert
                type="info"
                showIcon
                message={`Restoring ${info.user.username} in the background`}
                description={
                  <Space direction="vertical" size={8} style={{ width: "100%" }}>
                    <span>
                      This can take several minutes for large backups. Keep this drawer open to see the result, or
                      come back later — the restore keeps running on the server.
                    </span>
                    <RestoreProgressView progress={progress} />
                  </Space>
                }
              />
            )}
            {phase !== "done" && (
              <Button
                type="primary"
                danger={overwrite}
                loading={phase === "applying"}
                disabled={
                  phase === "applying" ||
                  checking ||
                  preflight?.blocked === true ||
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

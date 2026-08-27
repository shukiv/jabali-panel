// MyProfileBackupCard — user-shell self-backup card. Generate full
// backup of the caller's account; list recent self-backups; download
// when a row is succeeded. Mirrors AdminBackupsPage data shape but
// scoped via /me/backups (auth-gated to caller's user_id).
import { useTranslation } from "react-i18next";
import { downloadUrl } from "../../utils/download";
import { Button, Card, Grid, Input, Select, Space, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { getActAs } from "../../impersonation";
import { shortDateTime } from "../../utils/datetime";
import { backupTypeColor, backupTypeLabel } from "../../utils/backupType";
import { RowActions } from "../../components/RowActions";
import { DeleteOutlined, DownloadOutlined, ReloadOutlined, SaveOutlined, WarningOutlined } from "@icons";
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { useListQuery } from "../../hooks/useQueries";
import { extractApiError } from "../../apiErrors";
import { humanBytes as formatBytes } from "../../utils/bytes";
import { RestoreDrawer } from "./RestoreDrawer";

type MyBackup = {
  id: string;
  kind?: string;
  status: string;
  content?: string;
  bytes_total: number;
  bytes_added: number;
  created_at: string;
  error_text?: string;
};

// GH #1044: restore jobs share this list (kind=account_restore). A restore is
// not a downloadable/deletable backup artifact and has no size — its row drops
// the backup-only bits.
const isRestoreRow = (row: MyBackup): boolean =>
  row.kind === "account_restore" || row.kind === "system_restore";

const statusColor = (status: string): string => {
  switch (status) {
    case "succeeded":
      return "green";
    case "running":
      return "blue";
    case "failed":
      return "red";
    case "partial":
      return "gold";
    default:
      return "default";
  }
};

export const MyProfileBackupCard = () => {
  const { t } = useTranslation();
  const screens = Grid.useBreakpoint();
  const [submitting, setSubmitting] = useState(false);
  const [restoreId, setRestoreId] = useState<string | null>(null);
  const [content, setContent] = useState<string>("full");
  const [compression, setCompression] = useState<string>("");
  const [destinationId, setDestinationId] = useState<string>("");
  const query = useListQuery<MyBackup>({ resource: "me/backups" });

  // Destinations this tenant may target (GH #454). The backend returns only
  // the kinds allowed by the hosting package (id/name/kind — no secrets), plus
  // allow_local for the plain local default.
  const destQuery = useQuery({
    queryKey: ["me-backup-destinations"],
    queryFn: async () =>
      (
        await apiClient.get<{ data: { id: string; name: string; kind: string }[]; allow_local: boolean }>(
          "/me/backups/destinations",
        )
      ).data,
  });
  const dests = destQuery.data?.data ?? [];
  const allowLocal = destQuery.data?.allow_local ?? false;
  const destOptions = [
    ...(allowLocal ? [{ value: "", label: "Local (default)" }] : []),
    ...dests.map((d) => ({ value: d.id, label: `${d.name} (${d.kind})` })),
  ];

  // GH #454: tenant-editable backup exclusions (~/.backupignore). Loaded from
  // and saved to the caller's own home file; layered onto the global exclude
  // list at backup time. `exclDirty` stops a background refetch from clobbering
  // in-progress edits.
  const [exclusions, setExclusions] = useState<string>("");
  const [exclDirty, setExclDirty] = useState(false);
  const [savingExcl, setSavingExcl] = useState(false);
  const exclQuery = useQuery({
    queryKey: ["me-backup-exclusions"],
    queryFn: async () =>
      (await apiClient.get<{ patterns: string }>("/me/backups/exclusions")).data,
  });
  useEffect(() => {
    if (exclQuery.data && !exclDirty) setExclusions(exclQuery.data.patterns ?? "");
  }, [exclQuery.data, exclDirty]);

  const handleSaveExclusions = async () => {
    setSavingExcl(true);
    try {
      await apiClient.put("/me/backups/exclusions", { patterns: exclusions });
      feedback.message.success("Backup exclusions saved");
      setExclDirty(false);
      exclQuery.refetch();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Save failed"));
    } finally {
      setSavingExcl(false);
    }
  };

  const handleCreate = async () => {
    setSubmitting(true);
    try {
      await apiClient.post("/me/backups", { content, compression, destination_id: destinationId });
      feedback.message.success("Backup queued");
      query.refetch();
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Create failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await apiClient.delete(`/me/backups/${id}`);
      feedback.message.success("Backup deleted");
      query.refetch();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Delete failed"));
    }
  };

  const backupActions = (
        <Space wrap>
          <Select
            value={content}
            onChange={setContent}
            style={{ minWidth: 150 }}
            options={[
              { value: "full", label: "Full account" },
              { value: "files", label: "Files only" },
              { value: "database", label: "Databases only" },
            ]}
          />
          <Select
            value={compression}
            onChange={setCompression}
            style={{ minWidth: 140 }}
            options={[
              { value: "", label: "Auto compression" },
              { value: "max", label: "Max compression" },
              { value: "off", label: "No compression" },
            ]}
          />
          {destOptions.length > 0 && (
            <Select
              value={destinationId}
              onChange={setDestinationId}
              style={{ minWidth: 180 }}
              placeholder={t("myprofilebackupcard.destination")}
              options={destOptions}
            />
          )}
          <Button
            type="primary"
            loading={submitting}
            disabled={destOptions.length === 0}
            onClick={handleCreate}
          >
            Generate backup
          </Button>
        </Space>
  );

  return (
    <Card
      title={
        <span>
          <SaveOutlined style={{ marginRight: 8 }} />
          My backups
        </span>
      }
      extra={screens.md ? backupActions : undefined}
    >
      {!screens.md ? (
        <div style={{ width: "100%", marginBottom: 12 }}>{backupActions}</div>
      ) : null}
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        A full backup bundles your home directory, databases, and mailboxes
        into a portable tar.zst you can download. Backups are deduplicated
        on the server, so repeat runs only store what actually changed.
      </Typography.Paragraph>
      <Table<MyBackup>
        rowKey="id"
        loading={query.isLoading}
        dataSource={query.items ?? []}
        pagination={{ defaultPageSize: 10 }}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Created",
            dataIndex: "created_at",
            render: (t: string) => shortDateTime(t),
          },
          {
            // GH #454: show what each backup captured (full / database /
            // files). GH #1044: the kind is REAL, not assumed — restore
            // jobs share this list (kind=account_restore) and were being
            // labelled "Account Backup", which is exactly what the
            // reporter flagged.
            title: "Type",
            dataIndex: "content",
            render: (_: unknown, row: MyBackup) => (
              <Tag color={backupTypeColor(row.kind ?? "account_backup", row.content)}>
                {backupTypeLabel(row.kind ?? "account_backup", row.content)}
              </Tag>
            ),
          },
          {
            // A "partial" backup completed but a stage (e.g. the home dir)
            // failed — surface the reason so a tenant doesn't restore an
            // incomplete backup thinking it is whole (GH #454).
            title: "Status",
            dataIndex: "status",
            render: (s: string, row: MyBackup) => (
              <Space size={4}>
                <Tag color={statusColor(s)}>{s}</Tag>
                {row.error_text && (
                  <Tooltip title={row.error_text}>
                    <WarningOutlined style={{ color: "#faad14" }} />
                  </Tooltip>
                )}
              </Space>
            ),
          },
          {
            title: "Size",
            dataIndex: "bytes_total",
            // GH #1044: a restore has no artifact size — the 0 read as noise.
            render: (n: number, row: MyBackup) => (isRestoreRow(row) ? "—" : formatBytes(n)),
          },
          {
            title: "Actions",
            key: "actions",
            render: (_, row) => (
              <RowActions
                actions={[
                  // GH #1044: Download + Restore only make sense on a backup
                  // artifact, not a restore-history row (no snapshot to download,
                  // not itself restorable). JAB-327: a `partial` backup has a
                  // valid snapshot too — a non-critical stage failed but the
                  // manifest was written — so it is DOWNLOADABLE (mirrors admin +
                  // the backend's succeeded|partial download gate; a snapshotless
                  // job is still hard-rejected server-side). Restore stays
                  // succeeded-only (a partial account backup is not a safe restore
                  // source).
                  ...(!isRestoreRow(row) && (row.status === "succeeded" || row.status === "partial")
                    ? [{ key: "download", label: "Download", icon: <DownloadOutlined />, onClick: () => { const act = getActAs(); downloadUrl(`/api/v1/me/backups/${row.id}/download${act ? `?act_as=${encodeURIComponent(act.id)}` : ""}`); } }]
                    : []),
                  ...(!isRestoreRow(row) && row.status === "succeeded"
                    ? [{ key: "restore", label: "Restore", icon: <ReloadOutlined />, onClick: () => setRestoreId(row.id) }]
                    : []),
                  {
                    key: "delete",
                    label: isRestoreRow(row) ? "Remove" : "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: () => handleDelete(row.id),
                    // A restore row owns no snapshots — deleting it only clears
                    // the history entry; say that instead of the backup copy.
                    confirm: isRestoreRow(row)
                      ? { title: "Remove this restore from the list?", description: "This clears the restore entry from your history. It does not undo the restore or touch any backup.", okText: "Remove" }
                      : { title: "Delete this backup?", description: "This permanently removes the backup's snapshots from the repository and cannot be undone.", okText: "Delete" },
                  },
                ]}
              />
            ),
          },
        ]}
      />
      <div style={{ marginTop: 20 }}>
        <Typography.Text strong>Backup exclusions</Typography.Text>
        <Typography.Paragraph type="secondary" style={{ marginTop: 4, marginBottom: 8 }}>
          Paths to skip in your backups — one pattern per line (restic exclude
          syntax). Ideal for regenerable directories, e.g. <code>cache/</code>,{" "}
          <code>node_modules/</code>, <code>vendor/</code>, <code>*.log</code>.
          Saved to <code>~/.backupignore</code> and applied to every backup of
          your account.
        </Typography.Paragraph>
        <Input.TextArea
          rows={5}
          value={exclusions}
          onChange={(e) => {
            setExclusions(e.target.value);
            setExclDirty(true);
          }}
          placeholder={"cache/\nnode_modules/\nvendor/\n*.log"}
          disabled={exclQuery.isLoading}
          style={{ fontFamily: "monospace" }}
        />
        <div style={{ marginTop: 8 }}>
          <Button
            icon={<SaveOutlined />}
            loading={savingExcl}
            disabled={!exclDirty}
            onClick={handleSaveExclusions}
          >
            Save exclusions
          </Button>
        </div>
      </div>
      <RestoreDrawer
        backupId={restoreId}
        open={restoreId !== null}
        onClose={() => setRestoreId(null)}
      />
    </Card>
  );
};

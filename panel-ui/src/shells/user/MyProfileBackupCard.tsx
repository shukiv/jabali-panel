// MyProfileBackupCard — user-shell self-backup card. Generate full
// backup of the caller's account; list recent self-backups; download
// when a row is succeeded. Mirrors AdminBackupsPage data shape but
// scoped via /me/backups (auth-gated to caller's user_id).
import { useTranslation } from "react-i18next";
import { downloadUrl } from "../../utils/download";
import { Button, Card, Grid, Select, Space, Table, Tag, Tooltip, Typography, message } from "antd";
import { getActAs } from "../../impersonation";
import { shortDateTime } from "../../utils/datetime";
import { backupTypeColor, backupTypeLabel } from "../../utils/backupType";
import { RowActions } from "../../components/RowActions";
import { DeleteOutlined, DownloadOutlined, ReloadOutlined, SaveOutlined, WarningOutlined } from "@icons";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { useListQuery } from "../../hooks/useQueries";
import { extractApiError } from "../../apiErrors";
import { humanBytes as formatBytes } from "../../utils/bytes";
import { RestoreDrawer } from "./RestoreDrawer";

type MyBackup = {
  id: string;
  status: string;
  content?: string;
  bytes_total: number;
  bytes_added: number;
  created_at: string;
  error_text?: string;
};

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

  const handleCreate = async () => {
    setSubmitting(true);
    try {
      await apiClient.post("/me/backups", { content, compression, destination_id: destinationId });
      message.success("Backup queued");
      query.refetch();
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Create failed");
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await apiClient.delete(`/me/backups/${id}`);
      message.success("Backup deleted");
      query.refetch();
    } catch (err) {
      message.error(extractApiError(err, "Delete failed"));
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
        pagination={{ pageSize: 10 }}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Created",
            dataIndex: "created_at",
            render: (t: string) => shortDateTime(t),
          },
          {
            // GH #454: show what each backup captured (full / database / files).
            // Tenant backups are always account_backup, so the label is
            // content-driven.
            title: "Type",
            dataIndex: "content",
            render: (_: unknown, row: MyBackup) => (
              <Tag color={backupTypeColor("account_backup", row.content)}>
                {backupTypeLabel("account_backup", row.content)}
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
            render: (n: number) => formatBytes(n),
          },
          {
            title: "Actions",
            key: "actions",
            render: (_, row) => (
              <RowActions
                actions={[
                  ...(row.status === "succeeded"
                    ? [
                        { key: "download", label: "Download", icon: <DownloadOutlined />, onClick: () => { const act = getActAs(); downloadUrl(`/api/v1/me/backups/${row.id}/download${act ? `?act_as=${encodeURIComponent(act.id)}` : ""}`); } },
                        { key: "restore", label: "Restore", icon: <ReloadOutlined />, onClick: () => setRestoreId(row.id) },
                      ]
                    : []),
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: () => handleDelete(row.id),
                    confirm: { title: "Delete this backup?", description: "This permanently removes the backup's snapshots from the repository and cannot be undone.", okText: "Delete" },
                  },
                ]}
              />
            ),
          },
        ]}
      />
      <RestoreDrawer
        backupId={restoreId}
        open={restoreId !== null}
        onClose={() => setRestoreId(null)}
      />
    </Card>
  );
};

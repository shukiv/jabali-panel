// UploadedBackupsTable — GH #1993. Account backups an admin uploaded from
// another server and kept here (RestoreFromUploadDrawer keeps every admin
// upload). Each can be restored again or deleted; its retention says how long
// it stays. Hidden while there are none.
import { useEffect } from "react";
import { Space, Table, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, RotateCcwOutlined } from "@icons";
import { feedback } from "../../../lib/feedback";
import { RowActions } from "../../../components/RowActions";
import { useDeleteMutation, useListQuery } from "../../../hooks/useQueries";
import { extractApiError } from "../../../apiErrors";
import { shortDateTime } from "../../../utils/datetime";
import type { UploadedBackup } from "../../../apiClient";
import { RestoreProgressView } from "./RestoreProgressView";

export const UPLOADED_BACKUPS_RESOURCE = "admin/uploaded-backups";

const formatBytes = (n: number): string => {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(1)} ${units[i]}`;
};

// keptUntil says how long the backup stays on this server.
function keptUntil(b: UploadedBackup): string {
  if (!b.file_present) {
    return b.retention === "delete_after_restore" && b.restore_status === "done"
      ? "Deleted after the restore"
      : "File missing";
  }
  switch (b.retention) {
    case "keep_7_days":
      return `Until ${shortDateTime(b.expires_at)}`;
    case "delete_after_restore":
      return "Until a restore succeeds";
    default:
      return "Until deleted";
  }
}

function LastRestore({ b }: { b: UploadedBackup }) {
  switch (b.restore_status) {
    case "restoring":
      return (
        <Space direction="vertical" size={0}>
          <Tag color="blue">Restoring into {b.restore_target}</Tag>
          <RestoreProgressView progress={b.restore_progress} compact />
        </Space>
      );
    case "done":
      return (
        <Space direction="vertical" size={0}>
          <Tag color="green">Restored into {b.restore_target}</Tag>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {shortDateTime(b.restored_at)}
          </Typography.Text>
        </Space>
      );
    case "failed":
      return (
        <Space direction="vertical" size={0}>
          <Tooltip title={b.restore_result?.error}>
            <Tag color="red">Restore into {b.restore_target} failed</Tag>
          </Tooltip>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {shortDateTime(b.restored_at)}
          </Typography.Text>
        </Space>
      );
    default:
      return <Typography.Text type="secondary">Not restored yet</Typography.Text>;
  }
}

export function UploadedBackupsTable({ onRestore }: { onRestore: (b: UploadedBackup) => void }) {
  const q = useListQuery<UploadedBackup>({
    resource: UPLOADED_BACKUPS_RESOURCE,
    params: { pageSize: 100 },
  });
  const del = useDeleteMutation({ resource: UPLOADED_BACKUPS_RESOURCE });

  // Follow a running restore (it may have been started by another admin).
  const restoring = q.items.some((b) => b.restore_status === "restoring");
  const { refetch } = q;
  useEffect(() => {
    if (!restoring) return;
    const t = window.setInterval(() => void refetch(), 5000);
    return () => window.clearInterval(t);
  }, [restoring, refetch]);

  if (q.items.length === 0) return null;

  const handleDelete = (b: UploadedBackup) =>
    del.mutate(
      { id: b.id },
      {
        onSuccess: () => feedback.message.success("Uploaded backup deleted"),
        onError: (err) => feedback.message.error(extractApiError(err, "Delete failed")),
      },
    );

  return (
    <div style={{ marginBottom: 24 }}>
      <Space size={8} align="center" style={{ marginBottom: 8 }}>
        <Typography.Title level={5} style={{ margin: 0 }}>
          Uploaded backups
        </Typography.Title>
        <Tag color="purple">Uploaded — created on another server</Tag>
      </Space>
      <Table<UploadedBackup>
        rowKey="id"
        size="small"
        loading={q.isLoading}
        dataSource={q.items}
        pagination={q.items.length > 10 ? { defaultPageSize: 10 } : false}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Account",
            render: (_: unknown, b) => (
              <Space direction="vertical" size={0}>
                <span>{b.account_username}</span>
                {b.account_email && (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {b.account_email}
                  </Typography.Text>
                )}
              </Space>
            ),
          },
          {
            title: "File",
            render: (_: unknown, b) => (
              <Space direction="vertical" size={0}>
                <span>{b.file_name || "—"}</span>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {formatBytes(b.size_bytes)}
                </Typography.Text>
              </Space>
            ),
          },
          {
            title: "Uploaded",
            render: (_: unknown, b) => shortDateTime(b.created_at),
          },
          {
            title: "Kept",
            render: (_: unknown, b) =>
              b.file_present ? keptUntil(b) : <Typography.Text type="secondary">{keptUntil(b)}</Typography.Text>,
          },
          {
            title: "Last restore",
            render: (_: unknown, b) => <LastRestore b={b} />,
          },
          {
            title: "Actions",
            render: (_: unknown, b) => (
              <RowActions
                actions={[
                  {
                    key: "restore",
                    label: "Restore",
                    icon: <RotateCcwOutlined />,
                    disabled: !b.file_present || b.restore_status === "restoring",
                    tooltip: !b.file_present
                      ? "The file is no longer on this server"
                      : b.restore_status === "restoring"
                        ? "A restore of this backup is running"
                        : undefined,
                    onClick: () => onRestore(b),
                  },
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    hidden: b.restore_status === "restoring",
                    onClick: () => handleDelete(b),
                    confirm: {
                      title: "Delete this uploaded backup?",
                      description:
                        "Removes the uploaded file from this server. Accounts already restored from it are not changed.",
                      okText: "Delete",
                    },
                  },
                ]}
              />
            ),
          },
        ]}
      />
    </div>
  );
}

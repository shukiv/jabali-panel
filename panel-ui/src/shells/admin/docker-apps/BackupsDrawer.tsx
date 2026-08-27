// BackupsDrawer — list + create + restore restic snapshots for one
// installed app. Phase 8.1 ships with env-var-based restic config
// (JABALI_RESTIC_REPO + JABALI_RESTIC_PASSWORD on the agent
// service unit); a 503 from the backend means the operator hasn't
// configured them yet.
import { useTranslation } from "react-i18next";
import { Alert, App, Button, Drawer, Popconfirm, Space, Table, Tag, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";

import { createBackup, listBackups, restoreBackup, type BackupRow } from "./api";

dayjs.extend(relativeTime);

interface Props {
  open: boolean;
  appId: string | null;
  onClose: () => void;
}

export const BackupsDrawer = ({ open, appId, onClose }: Props) => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();

  const backupsQ = useQuery({
    queryKey: ["docker-app-backups", appId],
    queryFn: () => listBackups(appId!),
    enabled: !!appId && open,
  });

  const create = useMutation({
    mutationFn: async () => createBackup(appId!),
    onSuccess: (r) => {
      message.success(`Snapshot ${r.snapshot_id?.slice(0, 8) ?? ""} created`);
      qc.invalidateQueries({ queryKey: ["docker-app-backups", appId] });
    },
    onError: (e: unknown) =>
      message.error(
        e instanceof Error
          ? e.message
          : "Backup failed (check JABALI_RESTIC_REPO / JABALI_RESTIC_PASSWORD on agent)",
      ),
  });

  const restore = useMutation({
    mutationFn: async (snapshotId: string) => restoreBackup(appId!, snapshotId),
    onSuccess: () => {
      message.success("Restore completed; container brought back up");
      qc.invalidateQueries({ queryKey: ["docker-apps-installed"] });
    },
    onError: (e: unknown) => message.error(e instanceof Error ? e.message : "Restore failed"),
  });

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={t("backupsdrawer.backups")}
      width="min(100%, 760px)"
      destroyOnClose
      extra={
        <Button type="primary" loading={create.isPending} onClick={() => create.mutate()}>
          New backup
        </Button>
      }
    >
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        <Alert
          type="info"
          showIcon
          message={t("backupsdrawer.phase_8_1_restic_via_env_vars")}
          description={
            <>
              Operator must set <code>JABALI_RESTIC_REPO</code> and{" "}
              <code>JABALI_RESTIC_PASSWORD</code> on the <code>jabali-agent.service</code>{" "}
              unit. Per-destination selection lands in Phase 8.2.
            </>
          }
        />
        <Table<BackupRow>
          rowKey="id"
          size="small"
          loading={backupsQ.isLoading}
          dataSource={backupsQ.data?.backups ?? []}
          pagination={{ defaultPageSize: 20 }}
          columns={[
            {
              title: "Snapshot",
              dataIndex: "id",
              render: (id: string) => <Typography.Text code>{id.slice(0, 8)}</Typography.Text>,
              width: 110,
            },
            {
              title: "Created",
              dataIndex: "time",
              render: (t: string) => (
                <Space direction="vertical" size={0}>
                  <Typography.Text>{dayjs(t).fromNow()}</Typography.Text>
                  <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                    {dayjs(t).format("YYYY-MM-DD HH:mm")}
                  </Typography.Text>
                </Space>
              ),
            },
            {
              title: "Tags",
              dataIndex: "tags",
              render: (tags: string[] = []) =>
                tags.map((t) => (
                  <Tag key={t} style={{ marginRight: 4 }}>
                    {t}
                  </Tag>
                )),
            },
            {
              title: "Actions",
              width: 130,
              render: (_, row) => (
                <Popconfirm
                  title={t("backupsdrawer.restore_this_snapshot")}
                  description={t("backupsdrawer.container_will_be_stopped_files_restored_con")}
                  okText={t("backupsdrawer.restore")}
                  okButtonProps={{ danger: true, loading: restore.isPending }}
                  onConfirm={() => restore.mutate(row.id)}
                >
                  <Button size="small" danger>
                    Restore
                  </Button>
                </Popconfirm>
              ),
            },
          ]}
        />
      </Space>
    </Drawer>
  );
};

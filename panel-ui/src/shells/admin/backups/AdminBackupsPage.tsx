// AdminBackupsPage — admin overview of every backup run.
// Scheduler-fired jobs roll up under their run_id (one parent row,
// expandable to per-user children). Manual creates render flat.
import { Badge, Button, Card, Space, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { downloadUrl } from "../../../utils/download";
import { backupTypeColor, backupTypeLabel, runScopeSummary } from "../../../utils/backupType";
import { shortDateTime } from "../../../utils/datetime";
import { useTabParam } from "../../../hooks/useTabParam";
import { RowActions } from "../../../components/RowActions";
import {
  DeleteOutlined,
  CalendarCheckOutlined,
  DownloadOutlined,
  FileTextOutlined,
  HardDriveUploadOutlined,
  KeyOutlined,
  PlusOutlined,
  RotateCcwOutlined,
  SaveOutlined,
  SettingOutlined,
  CloseOutlined,
  LifeBuoyOutlined,
} from "@icons";
import { useEffect, useRef, useState } from "react";

import { BackupStatusTag } from "./BackupStatusTag";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";
import { useListQuery } from "../../../hooks/useQueries";
import { BackupLogModal } from "./BackupLogModal";
import { BackupLogsTab } from "./BackupLogsTab";

// GH #1044: restore jobs share the backup list (kind=account_restore). They own
// no snapshot, so their size/added read 0 and Download is meaningless.
const isRestoreKind = (kind: string): boolean =>
  kind === "account_restore" || kind === "system_restore";
import { BackupSettingsTab } from "./BackupSettingsTab";
import { CreateBackupDrawer } from "./CreateBackupDrawer";
import { DestinationsTab } from "./DestinationsTab";
import { EncryptionKeyCard } from "./EncryptionKeyCard";
import { SchedulesTab } from "./SchedulesTab";
import { RecoveryHandoffTab } from "./RecoveryHandoffTab";

interface BackupJob {
  id: string;
  user_id: string;
  destination_id?: string;
  kind: string;
  content?: string;
  status: string;
  systemd_unit: string;
  snapshot_id: string;
  bytes_added: number;
  bytes_total: number;
  created_at: string;
  finished_at?: string;
  error_text?: string;
  run_id?: string;
}

interface BackupRun {
  run_id: string;
  schedule_id?: string;
  has_accounts?: boolean;
  accounts?: number;
  kind: string;
  content?: string;
  total: number;
  succeeded: number;
  failed: number;
  running: number;
  queued: number;
  cancelled: number;
  partial: number;
  bytes_added: number;
  bytes_total: number;
  started_at: string;
  latest_updated: string;
}

interface RunsEnvelope {
  data: BackupRun[];
  manual: BackupJob[];
  total: number;
  manual_total: number;
}

interface RunRow {
  rowKey: string;
  isRun: true;
  run: BackupRun;
}
interface ManualRow {
  rowKey: string;
  isRun: false;
  job: BackupJob;
}
type TableRow = RunRow | ManualRow;

type TabKey =
  | "backups"
  | "destinations"
  | "schedules"
  | "encryption"
  | "settings"
  | "logs"
  | "recovery";

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

// Run summary collapses 6 status counters into a single Tag stack so
// the row at a glance answers "is this run still working / did any
// fail?" — full breakdown lives in the expanded child table.
const RunStatusSummary = ({ run }: { run: BackupRun }) => {
  const tags: { color: string; text: string }[] = [];
  if (run.running > 0) tags.push({ color: "blue", text: `${run.running} running` });
  if (run.queued > 0) tags.push({ color: "default", text: `${run.queued} queued` });
  if (run.failed > 0) tags.push({ color: "red", text: `${run.failed} failed` });
  if (run.partial > 0) tags.push({ color: "gold", text: `${run.partial} partial` });
  if (run.cancelled > 0) tags.push({ color: "default", text: `${run.cancelled} cancelled` });
  if (run.succeeded > 0) tags.push({ color: "green", text: `${run.succeeded} succeeded` });
  if (tags.length === 0) tags.push({ color: "default", text: `${run.total} jobs` });
  return (
    <Space size={4} wrap>
      {tags.map((t) => (
        <Tag key={t.text} color={t.color}>
          {t.text}
        </Tag>
      ))}
    </Space>
  );
};

export const AdminBackupsPage = () => {
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [logJob, setLogJob] = useState<BackupJob | null>(null);
  const [activeTab, setActiveTab] = useTabParam<TabKey>("backups");
  const [runs, setRuns] = useState<BackupRun[]>([]);
  const [manual, setManual] = useState<BackupJob[]>([]);
  const [loading, setLoading] = useState(false);
  const [runJobs, setRunJobs] = useState<Record<string, BackupJob[]>>({});
  // GH #502: control row expansion so the "Expand to manage" cell can toggle it
  // (previously only the +/- icon did, which the text falsely implied was clickable).
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  // Mirror runJobs into a ref so the polling reload (whose closure
  // captures state from effect-setup time) can read the LATEST set
  // of expanded runs, not a stale snapshot.
  const runJobsRef = useRef<Record<string, BackupJob[]>>({});
  useEffect(() => {
    runJobsRef.current = runJobs;
  }, [runJobs]);

  const usersQuery = useListQuery<{ id: string; username: string; email: string }>({
    resource: "users",
    params: { pageSize: 500 },
    enabled: activeTab === "backups",
  });
  const usernameById = (id: string): string => {
    if (id === "system") return "system";
    const u = (usersQuery.items ?? []).find((x) => x.id === id);
    return u?.username ?? id;
  };
  const destQuery = useListQuery<{ id: string; name: string; kind: string }>({
    resource: "admin/backup-destinations",
    params: { pageSize: 100 },
    enabled: activeTab === "backups",
  });
  const destNameById = (id?: string): string => {
    if (!id) return "—";
    const d = (destQuery.items ?? []).find((x) => x.id === id);
    return d?.name ?? id.slice(0, 8) + "…";
  };

  const reload = async () => {
    setLoading(true);
    try {
      const resp = await apiClient.get<RunsEnvelope>(
        "/admin/backup-runs?page_size=50",
      );
      setRuns(resp.data.data ?? []);
      setManual(resp.data.manual ?? []);
      // Refresh any currently-expanded run's child rows so polling
      // surfaces queued -> running -> succeeded transitions on the
      // child table without needing collapse/expand. Read the LATEST
      // runJobs via ref — the interval's closure would otherwise see
      // the empty {} from before the operator expanded anything.
      const expanded = Object.keys(runJobsRef.current);
      if (expanded.length > 0) {
        const updates = await Promise.all(
          expanded.map(async (id) => {
            try {
              const r = await apiClient.get<{ data: BackupJob[] }>(
                `/admin/backup-runs/${id}/jobs`,
              );
              return [id, r.data.data ?? []] as const;
            } catch {
              return [id, runJobsRef.current[id]] as const;
            }
          }),
        );
        setRunJobs((m) => {
          const next = { ...m };
          for (const [id, jobs] of updates) {
            next[id] = jobs;
          }
          return next;
        });
      }
    } catch (err) {
      feedback.message.error(extractApiError(err, "Load failed"));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (activeTab === "backups") {
      void reload();
    }
  }, [activeTab]);

  const hasActive =
    runs.some((r) => r.queued + r.running > 0) ||
    manual.some((j) => j.status === "queued" || j.status === "running");
  useEffect(() => {
    if (activeTab !== "backups") return;
    const interval = hasActive ? 3000 : 8000;
    const t = window.setInterval(() => {
      void reload();
    }, interval);
    return () => window.clearInterval(t);
  }, [activeTab, hasActive]);

  const expandRun = async (runID: string) => {
    // No cache short-circuit: re-fetch on every expand so the child
    // table reflects current state even after collapse/expand cycles
    // while polling was paused (e.g. tab switch).
    try {
      const resp = await apiClient.get<{ data: BackupJob[] }>(
        `/admin/backup-runs/${runID}/jobs`,
      );
      setRunJobs((m) => ({ ...m, [runID]: resp.data.data ?? [] }));
    } catch (err) {
      feedback.message.error(extractApiError(err, "Load run failed"));
    }
  };

  // GH #502: toggle a row's expansion from anywhere (the +/- icon and the
  // "Expand to manage" cell both call this). Lazy-loads a run's child jobs
  // on open, mirroring the icon's previous onExpand behaviour.
  const toggleRowExpand = (row: TableRow) => {
    setExpandedKeys((keys) => {
      if (keys.includes(row.rowKey)) {
        return keys.filter((k) => k !== row.rowKey);
      }
      if (row.isRun) void expandRun(row.run.run_id);
      return [...keys, row.rowKey];
    });
  };

  const handleDownload = (row: BackupJob) => {
    // GH #502: a `partial` backup has a valid snapshot too (a non-critical
    // stage failed but the manifest was written) — allow downloading it.
    if (row.status !== "succeeded" && row.status !== "partial") {
      feedback.message.warning("Backup must complete before download");
      return;
    }
    // GH #462: anchor-download, not window.location.href — navigating aborts the
    // in-flight backups poll and shows a spurious "Network Failed" toast.
    downloadUrl(`/api/v1/admin/backups/${row.id}/download`);
  };

  const handleCancel = async (row: BackupJob) => {
    try {
      await apiClient.post(`/admin/backups/${row.id}/cancel`);
      feedback.message.success(`Cancellation requested for ${row.id}`);
      void reload();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Cancel failed"));
    }
  };

  // Restore is account-only — system_backup snapshots are managed via
  // `jabali aide rebuild` / system_restore CLI flows, not the panel UI.
  // Submits the BackupJob's snapshot_id (== manifest snapshot) to the
  // existing POST /admin/backups/restore handler with overwrite=true,
  // which queues an account_restore job and dispatches it to the agent.
  const handleRestore = async (row: BackupJob) => {
    try {
      await apiClient.post(`/admin/backups/restore`, {
        manifest_snapshot_id: row.snapshot_id,
        target_user_id: row.user_id,
        overwrite: true,
        destination_id: row.destination_id,
      });
      feedback.message.success(`Restore queued for ${usernameById(row.user_id)}`);
      void reload();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Restore failed"));
    }
  };

  // Delete forgets + prunes the job's restic snapshots, then drops the row
  // (GH #294). Available for any status so failed/stale backups can be cleared.
  const handleDelete = async (row: BackupJob) => {
    try {
      await apiClient.delete(`/admin/backups/${row.id}`);
      feedback.message.success("Backup deleted");
      void reload();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Delete failed"));
    }
  };

  // GH #502: delete EVERY deletable job of a run behind one confirmation —
  // a Full Server run holds a job per account, and clearing hundreds of
  // jobs one dialog at a time made cleanup impractical. Running/queued
  // jobs are skipped server-side (cancel them first).
  const handleDeleteRun = async (runID: string) => {
    try {
      const resp = await apiClient.delete<{ deleted: number; skipped_running: number; failed: number }>(
        `/admin/backup-runs/${runID}/jobs`,
      );
      const { deleted, skipped_running, failed } = resp.data;
      if (failed > 0 || skipped_running > 0) {
        feedback.message.warning(
          `Deleted ${deleted} job(s); ${skipped_running} running/queued skipped, ${failed} failed`,
        );
      } else {
        feedback.message.success(`Deleted ${deleted} backup job(s)`);
      }
      void reload();
    } catch (err) {
      feedback.message.error(extractApiError(err, "Bulk delete failed"));
    }
  };

  const tableRows: TableRow[] = [
    ...runs.map<RunRow>((r) => ({ rowKey: `run:${r.run_id}`, isRun: true, run: r })),
    ...manual.map<ManualRow>((j) => ({ rowKey: `job:${j.id}`, isRun: false, job: j })),
  ].sort((a, b) => {
    const aT = a.isRun ? a.run.latest_updated : a.job.created_at;
    const bT = b.isRun ? b.run.latest_updated : b.job.created_at;
    return aT < bT ? 1 : aT > bT ? -1 : 0;
  });

  const renderChildJobs = (jobs: BackupJob[]) => (
    <Table<BackupJob>
      rowKey="id"
      dataSource={jobs}
      pagination={false}
      size="small"
      columns={[
        {
          title: "Job ID",
          dataIndex: "id",
          sorter: (a, b) => a.id.localeCompare(b.id),
          render: (id: string) => (
            <Tooltip title={id}>
              <code>{id.slice(0, 8)}…</code>
            </Tooltip>
          ),
        },
        {
          title: "User",
          dataIndex: "user_id",
          sorter: (a, b) => usernameById(a.user_id).localeCompare(usernameById(b.user_id)),
          render: (id: string) => usernameById(id),
        },
        {
          title: "Destination",
          dataIndex: "destination_id",
          sorter: (a, b) => (a.destination_id ?? "").localeCompare(b.destination_id ?? ""),
          render: (id?: string) => destNameById(id),
        },
        {
          title: "Status",
          dataIndex: "status",
          sorter: (a, b) => a.status.localeCompare(b.status),
          render: (s: string) => <BackupStatusTag status={s} />,
        },
        {
          title: "Added",
          dataIndex: "bytes_added",
          sorter: (a, b) => (a.bytes_added ?? 0) - (b.bytes_added ?? 0),
          render: (n: number) => formatBytes(n),
        },
        {
          title: "Size",
          dataIndex: "bytes_total",
          sorter: (a, b) => (a.bytes_total ?? 0) - (b.bytes_total ?? 0),
          render: (n: number) => formatBytes(n),
        },
        {
          title: "Actions",
          render: (_: unknown, row: BackupJob) => (
            <RowActions
              actions={[
                // GH #502: Download is the most common action after a backup completes —
                // make it the primary (first, visible) action; Log + the rest collapse
                // into the overflow menu.
                { key: "download", label: "Download", icon: <DownloadOutlined />, hidden: isRestoreKind(row.kind) || (row.status !== "succeeded" && row.status !== "partial"), onClick: () => handleDownload(row) },
                { key: "log", label: "Log", icon: <FileTextOutlined />, onClick: () => setLogJob(row) },
                {
                  key: "restore",
                  label: "Restore",
                  icon: <RotateCcwOutlined />,
                  danger: true,
                  hidden: !(row.status === "succeeded" && row.kind === "account_backup" && row.snapshot_id),
                  onClick: () => handleRestore(row),
                  confirm: { title: `Restore ${usernameById(row.user_id)}?`, description: "Overwrites the account's current files, databases, and mailboxes.", okText: "Restore" },
                },
                { key: "cancel", label: "Cancel", icon: <CloseOutlined />, danger: true, hidden: row.status !== "running", onClick: () => handleCancel(row) },
                {
                  key: "delete",
                  label: "Delete",
                  icon: <DeleteOutlined />,
                  danger: true,
                  hidden: row.status === "running",
                  onClick: () => handleDelete(row),
                  confirm: { title: "Delete this backup?", description: "Permanently removes this run's snapshots from the repository. Cannot be undone.", okText: "Delete" },
                },
              ]}
            />
          ),
        },
      ]}
    />
  );

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{ marginBottom: 16, width: "100%", justifyContent: "space-between" }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <SaveOutlined style={{ marginRight: 8 }} />
          Backups
        </Typography.Title>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setDrawerOpen(true)}
        >
          Create Backup
        </Button>
      </Space>

      <Card
        tabList={[
          {
            key: "backups",
            tab: (
              <Space size={6}>
                <RotateCcwOutlined />
                <span>Backups</span>
                <Badge count={runs.length + manual.length} showZero color="#999" />
              </Space>
            ),
          },
          {
            key: "destinations",
            tab: (
              <Space>
                <HardDriveUploadOutlined />
                Destinations
              </Space>
            ),
          },
          {
            key: "schedules",
            tab: (
              <Space>
                <CalendarCheckOutlined />
                Schedules
              </Space>
            ),
          },
          {
            key: "encryption",
            tab: (
              <Space>
                <KeyOutlined />
                Encryption key
              </Space>
            ),
          },
          {
            key: "settings",
            tab: (
              <Space>
                <SettingOutlined />
                Settings
              </Space>
            ),
          },
          {
            key: "logs",
            tab: (
              <Space>
                <FileTextOutlined />
                Logs
              </Space>
            ),
          },
          {
            key: "recovery",
            tab: (
              <Space>
                <LifeBuoyOutlined />
                Recovery
              </Space>
            ),
          },
        ]}
        activeTabKey={activeTab}
        onTabChange={(k) => setActiveTab(k as TabKey)}
      >
        {activeTab === "backups" && (
          <Table<TableRow>
            rowKey="rowKey"
            loading={loading}
            dataSource={tableRows}
            pagination={{ defaultPageSize: 25 }}
            scroll={{ x: "max-content" }}
            expandable={{
              expandedRowKeys: expandedKeys,
              onExpand: (_expanded, row) => toggleRowExpand(row),
              expandedRowRender: (row) =>
                row.isRun ? (
                  runJobs[row.run.run_id] ? (
                    renderChildJobs(runJobs[row.run.run_id])
                  ) : (
                    <Typography.Text type="secondary">Loading…</Typography.Text>
                  )
                ) : (
                  renderChildJobs([row.job])
                ),
            }}
            columns={[
              {
                title: "ID",
                sorter: (a, b) => (a.isRun ? a.run.run_id : a.job.id).localeCompare(b.isRun ? b.run.run_id : b.job.id),
                render: (_: unknown, row: TableRow) => {
                  const id = row.isRun ? row.run.run_id : row.job.id;
                  return (
                    <Tooltip title={id}>
                      <code>{id.slice(0, 8)}…</code>
                    </Tooltip>
                  );
                },
              },
              {
                title: "Source",
                // GH #502: manual vs scheduled is the run's schedule_id (set only
                // by the scheduler) — NOT whether jobs are grouped into a run. A
                // manual Full Server backup is a grouped run with no schedule_id,
                // and was wrongly labelled "scheduled run" before.
                sorter: (a, b) =>
                  Number(a.isRun && !!a.run.schedule_id) - Number(b.isRun && !!b.run.schedule_id),
                render: (_: unknown, row: TableRow) =>
                  row.isRun && row.run.schedule_id ? (
                    <Tag color="geekblue">scheduled run</Tag>
                  ) : (
                    <Tag>manual</Tag>
                  ),
              },
              {
                title: "Type",
                sorter: (a, b) => (a.isRun ? a.run.kind : a.job.kind).localeCompare(b.isRun ? b.run.kind : b.job.kind),
                render: (_: unknown, row: TableRow) => {
                  const kind = row.isRun ? row.run.kind : row.job.kind;
                  const content = row.isRun ? row.run.content : row.job.content;
                  const hasAccounts = row.isRun ? row.run.has_accounts : false;
                  const scope = row.isRun ? runScopeSummary(row.run.total, row.run.accounts ?? 0) : "";
                  return (
                    <Space direction="vertical" size={0}>
                      <Tag color={backupTypeColor(kind, content)}>{backupTypeLabel(kind, content, hasAccounts)}</Tag>
                      {scope && (
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {scope}
                        </Typography.Text>
                      )}
                    </Space>
                  );
                },
              },
              {
                title: "Status",
                render: (_: unknown, row: TableRow) =>
                  row.isRun ? (
                    <RunStatusSummary run={row.run} />
                  ) : (
                    <BackupStatusTag status={row.job.status} />
                  ),
              },
              {
                title: "Added (dedup win)",
                sorter: (a, b) => (a.isRun ? a.run.bytes_added : a.job.bytes_added) - (b.isRun ? b.run.bytes_added : b.job.bytes_added),
                render: (_: unknown, row: TableRow) =>
                  !row.isRun && isRestoreKind(row.job.kind)
                    ? "—"
                    : formatBytes(row.isRun ? row.run.bytes_added : row.job.bytes_added),
              },
              {
                title: "Logical size",
                sorter: (a, b) => (a.isRun ? a.run.bytes_total : a.job.bytes_total) - (b.isRun ? b.run.bytes_total : b.job.bytes_total),
                render: (_: unknown, row: TableRow) =>
                  !row.isRun && isRestoreKind(row.job.kind)
                    ? "—"
                    : formatBytes(row.isRun ? row.run.bytes_total : row.job.bytes_total),
              },
              {
                title: "Started",
                sorter: (a, b) =>
                  ((a.isRun ? a.run.started_at : a.job.created_at) ? +new Date(a.isRun ? a.run.started_at : a.job.created_at) : 0) -
                  ((b.isRun ? b.run.started_at : b.job.created_at) ? +new Date(b.isRun ? b.run.started_at : b.job.created_at) : 0),
                render: (_: unknown, row: TableRow) =>
                  shortDateTime(row.isRun ? row.run.started_at : row.job.created_at),
              },
              {
                title: "Actions",
                render: (_: unknown, row: TableRow) =>
                  row.isRun ? (
                    <Space>
                      <Typography.Link style={{ fontSize: 12 }} onClick={() => toggleRowExpand(row)}>
                        {expandedKeys.includes(row.rowKey) ? "Collapse" : "Expand to manage"}
                      </Typography.Link>
                      <RowActions
                        actions={[
                          {
                            key: "delete-all",
                            label: "Delete all jobs",
                            icon: <DeleteOutlined />,
                            danger: true,
                            onClick: () => handleDeleteRun(row.run.run_id),
                            confirm: {
                              title: `Delete all ${row.run.total} job(s) of this run?`,
                              description:
                                "Permanently removes every finished job's snapshots from the repository. Running or queued jobs are skipped. Cannot be undone.",
                              okText: "Delete all",
                            },
                          },
                        ]}
                      />
                    </Space>
                  ) : (
                    <RowActions
                      actions={[
                        {
                          key: "delete",
                          label: "Delete",
                          icon: <DeleteOutlined />,
                          danger: true,
                          hidden: row.job.status === "running",
                          onClick: () => handleDelete(row.job),
                          confirm: { title: "Delete this backup?", description: "Permanently removes this backup's snapshots from the repository. Cannot be undone.", okText: "Delete" },
                        },
                      ]}
                    />
                  ),
              },
            ]}
          />
        )}
        {activeTab === "destinations" && <DestinationsTab />}
        {activeTab === "schedules" && <SchedulesTab />}
        {activeTab === "encryption" && <EncryptionKeyCard />}
        {activeTab === "settings" && <BackupSettingsTab />}
        {activeTab === "logs" && <BackupLogsTab />}
        {activeTab === "recovery" && <RecoveryHandoffTab />}
      </Card>

      <CreateBackupDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        onCreated={() => {
          setDrawerOpen(false);
          void reload();
        }}
      />

      <BackupLogModal
        job={logJob}
        onClose={() => setLogJob(null)}
      />
    </div>
  );
};

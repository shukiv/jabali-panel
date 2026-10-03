// AdminSystemJobs — Admin → Cron Jobs → System jobs (GH #1686): the scheduled
// jobs Jabali installs on the server, kept apart from tenant cron jobs. Each
// row says what the job does, when it runs, whether it is scheduled, running
// or disabled, and how its last run went. Run now shows only where the server
// allows a manual run; jobs whose settings live on another page (Updates,
// Backups) link there instead of offering a second editor. Nothing here turns
// a job off: the security jobs stay on by design.
import { useState } from "react";
import { useNavigate } from "react-router";
import { Alert, Button, Card, Space, Table, Tag, Tooltip, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { useQuery } from "@tanstack/react-query";
import { EyeOutlined, LinkOutlined, PlayCircleOutlined, ReloadOutlined, ToolOutlined } from "@icons";

import {
  getAdminSystemJobLog,
  listAdminSystemJobs,
  runAdminSystemJob,
  type AdminSystemJob,
} from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { RowActions } from "../../../components/RowActions";
import { CronLogDrawer } from "../../../components/cron/CronLogDrawer";
import { humanizeSchedule } from "../../../utils/cronSchedule";

dayjs.extend(relativeTime);

const CATEGORY_LABEL: Record<string, string> = {
  security: "Security",
  maintenance: "Maintenance",
  mail: "Mail",
  backups: "Backups",
  statistics: "Statistics",
  updates: "Updates",
};

const STATUS: Record<AdminSystemJob["status"], { color: string; label: string }> = {
  scheduled: { color: "green", label: "Scheduled" },
  running: { color: "processing", label: "Running" },
  disabled: { color: "default", label: "Disabled" },
};

const MANAGED_BY: Record<NonNullable<AdminSystemJob["managed_by"]>, { label: string; path: string }> = {
  updates: { label: "Open Updates", path: "/jabali-admin/updates" },
  backups: { label: "Open Backups", path: "/jabali-admin/backups" },
};

const RESULT_COLOR: Record<string, string> = { success: "green", failed: "red" };

const errorCode = (e: unknown): string | undefined =>
  (e as { response?: { data?: { error?: string } } })?.response?.data?.error;

const exact = (ts: string) => dayjs(ts).format("YYYY-MM-DD HH:mm");

export const AdminSystemJobs = () => {
  const navigate = useNavigate();
  const [runningId, setRunningId] = useState<string | null>(null);
  const [logJob, setLogJob] = useState<AdminSystemJob | null>(null);

  const { data, isLoading, isError, refetch, isFetching } = useQuery({
    queryKey: ["admin-system-jobs"],
    queryFn: listAdminSystemJobs,
    // Poll faster while a job runs so its status and result update on their own.
    refetchInterval: (q) => (q.state.data?.data.some((j) => j.status === "running") ? 5000 : 60000),
  });
  const jobs = data?.data ?? [];

  const handleRun = async (job: AdminSystemJob) => {
    setRunningId(job.id);
    try {
      await runAdminSystemJob(job.id);
      feedback.message.success(`${job.label} started`);
    } catch (e) {
      if (errorCode(e) === "already_running") {
        feedback.message.info(`${job.label} is already running`);
      } else {
        feedback.message.error(`Could not start ${job.label}${errorCode(e) ? ` (${errorCode(e)})` : ""}`);
      }
    } finally {
      setRunningId(null);
      void refetch();
    }
  };

  const columns: ColumnsType<AdminSystemJob> = [
    {
      title: "Job",
      dataIndex: "label",
      render: (label: string, row) => (
        <Space direction="vertical" size={0} style={{ maxWidth: 420 }}>
          <Typography.Text strong>{label}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {row.description}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: "Category",
      dataIndex: "category",
      render: (c: string) => <Tag>{CATEGORY_LABEL[c] ?? c}</Tag>,
    },
    {
      title: "Schedule",
      dataIndex: "schedule",
      render: (s: string, row) => {
        // A disabled job does not run on its schedule, so the schedule is shown
        // as what it would be, not as an active one (GH #1686). Run now still
        // works and still updates Last run.
        if (row.status === "disabled") {
          const when = row.schedule_format === "cron" ? humanizeSchedule(s) : s;
          return (
            <Space direction="vertical" size={0}>
              <Typography.Text type="secondary">Not scheduled</Typography.Text>
              {when && (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {when} when enabled
                </Typography.Text>
              )}
            </Space>
          );
        }
        return row.schedule_format === "cron" ? (
          <Tooltip title={s}>
            <Tag>{humanizeSchedule(s)}</Tag>
          </Tooltip>
        ) : (
          s || "—"
        );
      },
    },
    {
      title: "Status",
      dataIndex: "status",
      render: (s: AdminSystemJob["status"]) => <Tag color={STATUS[s].color}>{STATUS[s].label}</Tag>,
    },
    {
      title: "Last run",
      dataIndex: "last_run_at",
      render: (ts: string | null, row) => {
        if (row.last_result === "running") {
          return <Tag color="processing">running now</Tag>;
        }
        if (!ts) {
          return <Tag>never</Tag>;
        }
        const tag = <Tag color={RESULT_COLOR[row.last_result] ?? "default"}>{dayjs(ts).fromNow()}</Tag>;
        const result = row.last_result === "failed" ? "failed" : row.last_result === "success" ? "succeeded" : "";
        return <Tooltip title={`${exact(ts)}${result ? ` · ${result}` : ""}`}>{tag}</Tooltip>;
      },
    },
    {
      title: "Next run",
      dataIndex: "next_run_at",
      render: (ts: string | null) =>
        ts ? (
          <Tooltip title={exact(ts)}>
            <span>{dayjs(ts).fromNow()}</span>
          </Tooltip>
        ) : (
          "—"
        ),
    },
    {
      title: "Actions",
      dataIndex: "actions",
      render: (_, row) => (
        <RowActions
          actions={[
            {
              key: "run",
              label: "Run now",
              icon: <PlayCircleOutlined />,
              hidden: !row.can_run_now,
              loading: runningId === row.id,
              disabled: row.status === "running",
              tooltip: row.status === "running" ? "Already running" : undefined,
              confirm: { title: `Run "${row.label}" now?`, description: row.description, okText: "Run now" },
              onClick: () => void handleRun(row),
            },
            {
              key: "open",
              label: row.managed_by ? MANAGED_BY[row.managed_by].label : "",
              icon: <LinkOutlined />,
              hidden: !row.managed_by,
              onClick: () => row.managed_by && navigate(MANAGED_BY[row.managed_by].path),
            },
            {
              key: "log",
              label: "View log",
              icon: <EyeOutlined />,
              hidden: !row.has_log,
              onClick: () => setLogJob(row),
            },
          ]}
        />
      ),
    },
  ];

  return (
    <div>
      <Space wrap align="center" style={{ marginBottom: 8, width: "100%", justifyContent: "space-between" }}>
        <Typography.Title level={3} style={{ margin: 0 }}>
          <ToolOutlined /> System jobs
        </Typography.Title>
        <Space>
          <Typography.Text type="secondary">
            {jobs.length} job{jobs.length === 1 ? "" : "s"}
          </Typography.Text>
          <Button icon={<ReloadOutlined />} onClick={() => void refetch()} loading={isFetching && !isLoading}>
            Refresh
          </Button>
        </Space>
      </Space>
      <Typography.Paragraph type="secondary">
        Scheduled jobs Jabali runs on this server. Security jobs always stay on. Jobs that are set up on another page
        link there.
      </Typography.Paragraph>
      {isError && <Alert type="error" showIcon message="Could not load the system jobs." style={{ marginBottom: 16 }} />}
      <Card>
        <Table<AdminSystemJob>
          rowKey="id"
          columns={columns}
          dataSource={jobs}
          loading={isLoading}
          pagination={false}
          size="small"
          scroll={{ x: "max-content" }}
        />
      </Card>
      <CronLogDrawer
        open={logJob !== null}
        onClose={() => setLogJob(null)}
        jobId={logJob?.id ?? ""}
        title={logJob ? `${logJob.label}: log` : undefined}
        fetchLog={logJob ? (lines) => getAdminSystemJobLog(logJob.id, lines) : undefined}
      />
    </div>
  );
};

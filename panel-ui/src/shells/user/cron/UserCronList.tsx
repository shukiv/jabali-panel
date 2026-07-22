import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import {
  App,
  Button,
  Card,
  Input,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  CalendarCheckOutlined,
  PlusSquareOutlined,
  DeleteOutlined,
  EditOutlined,
  PlayCircleOutlined,
  EyeOutlined,
  CheckOutlined,
  CloseOutlined,
} from "@icons";
import { RowActions } from "../../../components/RowActions";
import { useQuery } from "@tanstack/react-query";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import {
  listCronJobs,
  deleteCronJob,
  updateCronJob,
  runCronJobNow,
  type CronJob,
} from "../../../apiClient";
import { CreateCronModal } from "./CreateCronModal";
import { CronLogDrawer } from "./CronLogDrawer";
import { RunNowResultModal } from "./RunNowResultModal";

dayjs.extend(relativeTime);

const SCHEDULE_PRESETS: Record<string, string> = {
  "* * * * *": "Every minute",
  "0 * * * *": "Hourly",
  "0 3 * * *": "Daily at 3 AM",
  "0 3 * * 0": "Weekly (Sun 3 AM)",
  "0 3 1 * *": "Monthly (1st 3 AM)",
};

const humanizeSchedule = (schedule: string): string => {
  return SCHEDULE_PRESETS[schedule] || schedule;
};

export const UserCronList = () => {
  const { t } = useTranslation();
  const { message: antMessage } = App.useApp();
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [editingJob, setEditingJob] = useState<CronJob | null>(null);
  const [logDrawerOpen, setLogDrawerOpen] = useState(false);
  const [logJobId, setLogJobId] = useState<string | null>(null);
  const [runNowModalOpen, setRunNowModalOpen] = useState(false);
  const [runNowResult, setRunNowResult] = useState<{
    exit_code: number;
    stdout: string;
    stderr: string;
  } | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [runningId, setRunningId] = useState<string | null>(null);
  const [togglingId, setTogglingId] = useState<string | null>(null);

  const {
    data: listResponse = { items: [] },
    isLoading,
    refetch,
  } = useQuery({
    queryKey: ["cron-jobs"],
    queryFn: async () => listCronJobs(),
  });

  const jobs = listResponse.items || [];
  const [search, setSearch] = useState("");
  const filteredJobs = useMemo(() => {
    if (!search) return jobs;
    const needle = search.toLowerCase();
    return jobs.filter(
      (j) =>
        j.name.toLowerCase().includes(needle) ||
        j.command.toLowerCase().includes(needle) ||
        j.schedule.toLowerCase().includes(needle),
    );
  }, [jobs, search]);

  const handleOpenCreateModal = () => {
    setEditingJob(null);
    setCreateModalOpen(true);
  };

  const handleOpenEditModal = (job: CronJob) => {
    setEditingJob(job);
    setCreateModalOpen(true);
  };

  const handleCreateSuccess = () => {
    setCreateModalOpen(false);
    setEditingJob(null);
    refetch();
  };

  const handleDelete = async (job: CronJob) => {
    setDeletingId(job.id);
    try {
      await deleteCronJob(job.id);
      antMessage.success("Cron job deleted successfully");
      refetch();
    } catch (error) {
      const msg =
        (error as { response?: { data?: { detail?: string } }; message?: string })
          ?.response?.data?.detail ??
        (error as { message?: string })?.message ??
        "Failed to delete cron job";
      antMessage.error(msg);
    } finally {
      setDeletingId(null);
    }
  };

  const handleRunNow = async (job: CronJob) => {
    setRunningId(job.id);
    try {
      const result = await runCronJobNow(job.id);
      setRunNowResult(result);
      setRunNowModalOpen(true);
      // Refetch to update last_run_at and last_exit_code
      setTimeout(() => refetch(), 2000);
    } catch (error) {
      const msg =
        (error as { response?: { data?: { detail?: string } }; message?: string })
          ?.response?.data?.detail ??
        (error as { message?: string })?.message ??
        "Failed to run cron job";
      antMessage.error(msg);
    } finally {
      setRunningId(null);
    }
  };

  const handleToggleEnabled = async (job: CronJob) => {
    setTogglingId(job.id);
    try {
      await updateCronJob(job.id, { enabled: !job.enabled });
      antMessage.success(
        job.enabled ? "Cron job disabled" : "Cron job enabled"
      );
      refetch();
    } catch (error) {
      const msg =
        (error as { response?: { data?: { detail?: string } }; message?: string })
          ?.response?.data?.detail ??
        (error as { message?: string })?.message ??
        "Failed to update cron job";
      antMessage.error(msg);
    } finally {
      setTogglingId(null);
    }
  };

  const truncateCommand = (cmd: string): string => {
    if (cmd.length <= 40) return cmd;
    return cmd.substring(0, 40) + "…";
  };

  const isLoading_ = isLoading || deletingId !== null;

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
        }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <CalendarCheckOutlined /> Cron Jobs
        </Typography.Title>
        <Button
          type="primary"
          icon={<PlusSquareOutlined />}
          onClick={handleOpenCreateModal}
        >
          New Cron Job
        </Button>
      </Space>

      <Card>
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Input.Search
          placeholder={t("usercronlist.search_by_name_command_or_schedule")}
          allowClear
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          onSearch={(value) => setSearch(value.trim())}
          style={{ maxWidth: 360 }}
        />
        <Table<CronJob>
        dataSource={filteredJobs}
        loading={isLoading_}
        rowKey="id"
        pagination={false}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Name",
            dataIndex: "name",
            sorter: (a, b) => a.name.localeCompare(b.name),
            defaultSortOrder: "ascend",
          },
          {
            title: "Schedule",
            dataIndex: "schedule",
            sorter: (a, b) => a.schedule.localeCompare(b.schedule),
            render: (schedule: string) => (
              <Tooltip title={schedule}>
                <span>{humanizeSchedule(schedule)}</span>
              </Tooltip>
            ),
          },
          {
            title: "Command",
            dataIndex: "command",
            sorter: (a, b) => a.command.localeCompare(b.command),
            render: (command: string) => (
              <Tooltip title={command}>
                <span
                  style={{
                    fontFamily: "monospace",
                  }}
                >
                  {truncateCommand(command)}
                </span>
              </Tooltip>
            ),
          },
          {
            title: "Last Run",
            dataIndex: "last_run_at",
            sorter: (a, b) => (a.last_run_at ? +new Date(a.last_run_at) : 0) - (b.last_run_at ? +new Date(b.last_run_at) : 0),
            render: (lastRunAt: string | null) =>
              lastRunAt ? dayjs(lastRunAt).fromNow() : "Never",
          },
          {
            title: "Last Exit",
            dataIndex: "last_exit_code",
            sorter: (a, b) => (a.last_exit_code ?? -1) - (b.last_exit_code ?? -1),
            render: (code: number | null) => {
              if (code === null) return <Typography.Text type="secondary">—</Typography.Text>;
              if (code === 0)
                return <Tag color="green">{code}</Tag>;
              return <Tag color="red"><code>{code}</code></Tag>;
            },
          },
          {
            title: "Enabled",
            dataIndex: "enabled",
            sorter: (a, b) => Number(a.enabled) - Number(b.enabled),
            render: (enabled: boolean, record) => (
              <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />}
                checked={enabled}
                onChange={() => handleToggleEnabled(record)}
                loading={togglingId === record.id}
                disabled={togglingId !== null && togglingId !== record.id}
              />
            ),
          },
          {
            title: "Actions",
            dataIndex: "actions",
            render: (_, record) => (
              <RowActions
                actions={[
                  {
                    key: "run",
                    label: "Run now",
                    icon: <PlayCircleOutlined />,
                    onClick: () => handleRunNow(record),
                    loading: runningId === record.id,
                    disabled: runningId !== null && runningId !== record.id,
                  },
                  {
                    key: "log",
                    label: "Log",
                    icon: <EyeOutlined />,
                    onClick: () => { setLogJobId(record.id); setLogDrawerOpen(true); },
                  },
                  {
                    key: "edit",
                    label: "Edit",
                    icon: <EditOutlined />,
                    onClick: () => handleOpenEditModal(record),
                  },
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    loading: deletingId === record.id,
                    onClick: () => handleDelete(record),
                    confirm: { title: "Delete Cron Job", description: "Are you sure you want to delete this cron job?", okText: "Yes" },
                  },
                ]}
              />
            ),
          },
        ]}
        />
        </Space>
      </Card>

      <CreateCronModal
        open={createModalOpen}
        onClose={() => {
          setCreateModalOpen(false);
          setEditingJob(null);
        }}
        onSuccess={handleCreateSuccess}
        initial={editingJob}
      />

      {logJobId && (
        <CronLogDrawer
          open={logDrawerOpen}
          onClose={() => {
            setLogDrawerOpen(false);
            setLogJobId(null);
          }}
          jobId={logJobId}
        />
      )}

      <RunNowResultModal
        open={runNowModalOpen}
        onClose={() => {
          setRunNowModalOpen(false);
          setRunNowResult(null);
        }}
        result={runNowResult}
      />
    </div>
  );
};

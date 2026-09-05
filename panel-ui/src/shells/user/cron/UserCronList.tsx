// UserCronList — tenant cron list, now a thin Adapter over the shared
// CronJobWorkspace Module (JAB-298). It selects the tenant list operation,
// owner-free search, this screen's column order (separate Last Run and Last
// Exit columns), an unpaginated table, an Edit action, and the tenant
// create/edit editor. Toggle / run / delete / log / overlays live in the Module.
import { Tag, Tooltip, Typography } from "antd";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";

import { listCronJobs } from "../../../apiClient";
import { CronJobWorkspace, type CronWorkspaceAudience } from "../../../components/cron/CronJobWorkspace";
import { cronActionsColumn, cronEnabledColumn } from "../../../components/cron/cronColumns";
import { CreateCronModal } from "./CreateCronModal";
import { humanizeSchedule } from "../../../utils/cronSchedule";

dayjs.extend(relativeTime);

const truncateCommand = (cmd: string): string => (cmd.length <= 40 ? cmd : cmd.substring(0, 40) + "…");

export const UserCronList = () => {
  const audience: CronWorkspaceAudience = {
    title: "Cron Jobs",
    showCount: false,
    query: { key: ["cron-jobs"], fn: () => listCronJobs() },
    searchPlaceholder: "usercronlist.search_by_name_command_or_schedule",
    searchOwner: false,
    pagination: false,
    renderEditor: ({ open, editing, onClose, onSuccess }) => (
      <CreateCronModal open={open} onClose={onClose} onSuccess={onSuccess} initial={editing} />
    ),
    buildColumns: (ctx) => [
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
            <span style={{ fontFamily: "monospace" }}>{truncateCommand(command)}</span>
          </Tooltip>
        ),
      },
      {
        title: "Last Run",
        dataIndex: "last_run_at",
        sorter: (a, b) =>
          (a.last_run_at ? +new Date(a.last_run_at) : 0) - (b.last_run_at ? +new Date(b.last_run_at) : 0),
        render: (lastRunAt: string | null) => (lastRunAt ? dayjs(lastRunAt).fromNow() : "Never"),
      },
      {
        title: "Last Exit",
        dataIndex: "last_exit_code",
        sorter: (a, b) => (a.last_exit_code ?? -1) - (b.last_exit_code ?? -1),
        render: (code: number | null) => {
          if (code === null) return <Typography.Text type="secondary">—</Typography.Text>;
          if (code === 0) return <Tag color="green">{code}</Tag>;
          return (
            <Tag color="red">
              <code>{code}</code>
            </Tag>
          );
        },
      },
      cronEnabledColumn(ctx),
      cronActionsColumn(ctx, {
        runLabel: "Run now",
        canEdit: true,
        deleteConfirm: () => ({
          title: "Delete Cron Job",
          description: "Are you sure you want to delete this cron job?",
          okText: "Yes",
        }),
      }),
    ],
  };

  return <CronJobWorkspace audience={audience} />;
};

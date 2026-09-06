// AdminCronList — admin-side cross-tenant cron view (GH#127), now a thin
// Adapter over the shared CronJobWorkspace Module (JAB-298). It selects the
// admin list operation, owner-aware search, the Owner sub-line, this screen's
// column order (Enabled before the merged Last-run tag), paginated table, and
// the admin create-as-user editor. Toggle / run / delete / log / overlays all
// live in the Module; they authorise admins server-side via
// fetchAndAuthorize's claims.IsAdmin bypass.
import { useTranslation } from "react-i18next";
import { Space, Tag, Tooltip, Typography } from "antd";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";

import { listAdminCronJobs } from "../../../apiClient";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { CronJobWorkspace, type CronWorkspaceAudience } from "../../../components/cron/CronJobWorkspace";
import {
  cronActionsColumn,
  cronEnabledColumn,
  type CronWorkspaceRow,
} from "../../../components/cron/cronColumns";
import { AdminCreateCronModal } from "./AdminCreateCronModal";
import { humanizeSchedule } from "../../../utils/cronSchedule";

dayjs.extend(relativeTime);

const truncate = (s: string, n = 40) => (s.length <= n ? s : s.substring(0, n) + "…");

export const AdminCronList = () => {
  const { t } = useTranslation();

  const audience: CronWorkspaceAudience = {
    title: "Cron Jobs (all tenants)",
    showCount: true,
    query: { key: ["admin-cron-jobs"], fn: () => listAdminCronJobs() },
    searchPlaceholder: "admincronlist.search_by_name_command_schedule_or_owner",
    searchOwner: true,
    pagination: { defaultPageSize: 25, showSizeChanger: true },
    tableSize: "small",
    renderEmpty: (openCreate) => (
      <EmptyWithCTA
        description={t("admincronlist.no_cron_jobs_yet")}
        ctaLabel="Create cron job"
        onCta={openCreate}
      />
    ),
    renderEditor: ({ open, onClose, onSuccess }) => (
      <AdminCreateCronModal open={open} onClose={onClose} onSuccess={onSuccess} />
    ),
    buildColumns: (ctx) => [
      {
        title: "Name",
        dataIndex: "name",
        sorter: (a, b) => a.name.localeCompare(b.name),
        defaultSortOrder: "ascend",
        render: (name: string, row: CronWorkspaceRow) => (
          <Space direction="vertical" size={0}>
            <Typography.Text strong>{name}</Typography.Text>
            <Tooltip title={`user_id: ${row.user_id}`}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {row.username || row.user_id}
              </Typography.Text>
            </Tooltip>
          </Space>
        ),
      },
      {
        title: "Schedule",
        dataIndex: "schedule",
        sorter: (a, b) => a.schedule.localeCompare(b.schedule),
        width: 200,
        render: (sch: string) => (
          <Tooltip title={sch}>
            <Tag>{humanizeSchedule(sch)}</Tag>
          </Tooltip>
        ),
      },
      {
        title: "Command",
        dataIndex: "command",
        sorter: (a, b) => a.command.localeCompare(b.command),
        render: (cmd: string) => (
          <Tooltip title={cmd}>
            <Typography.Text code>{truncate(cmd)}</Typography.Text>
          </Tooltip>
        ),
      },
      cronEnabledColumn(ctx),
      {
        title: "Last run",
        dataIndex: "last_run_at",
        sorter: (a, b) =>
          (a.last_run_at ? +new Date(a.last_run_at) : 0) - (b.last_run_at ? +new Date(b.last_run_at) : 0),
        width: 160,
        render: (ts: string | null, row: CronWorkspaceRow) =>
          ts ? (
            <Tooltip title={`exit=${row.last_exit_code ?? "?"}${row.last_error ? "\n" + row.last_error : ""}`}>
              <Tag color={row.last_exit_code === 0 ? "green" : row.last_exit_code === null ? "default" : "red"}>
                {dayjs(ts).fromNow()}
              </Tag>
            </Tooltip>
          ) : (
            <Tag>never</Tag>
          ),
      },
      cronActionsColumn(ctx, {
        runLabel: "Run",
        canEdit: false,
        deleteConfirm: (row) => ({ title: `Delete cron job "${row.name}"?`, okText: "Delete" }),
      }),
    ],
  };

  return <CronJobWorkspace audience={audience} />;
};

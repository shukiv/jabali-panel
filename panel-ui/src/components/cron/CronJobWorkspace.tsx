// CronJobWorkspace — the shared cron-job list Module for the admin and tenant
// shells (JAB-298). The two screens were near-copies: same react-query load,
// same toggle/run/delete/log orchestration, same per-row busy state, same
// backend-detail error surfacing, and the same log + run-result overlays. They
// differed only in list operation, owner-aware search, descriptive columns,
// pagination, and the editor (admin creates-as-user; tenant creates + edits).
//
// The Module owns everything shared — query wiring, search, the action handlers
// with their one busy-state implementation, the New button, the editor state
// machine, and the overlays. An audience Adapter selects the list/owner/editor
// policy and its column ORDER, splicing in the shared Enabled / Actions columns
// from cronColumns so neither screen's presentation drifts.
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  App,
  Button,
  Card,
  Input,
  Space,
  Table,
  Typography,
  type TableColumnsType,
  type TablePaginationConfig,
} from "antd";
import { CalendarCheckOutlined, PlusSquareOutlined } from "@icons";
import { useQuery } from "@tanstack/react-query";

import { deleteCronJob, runCronJobNow, updateCronJob, type CronRunNowResponse } from "../../apiClient";
import { CronLogDrawer } from "./CronLogDrawer";
import { RunNowResultModal } from "./RunNowResultModal";
import type { CronColumnContext, CronWorkspaceRow } from "./cronColumns";

export type { CronWorkspaceRow, CronColumnContext } from "./cronColumns";

export interface CronEditorRenderProps {
  open: boolean;
  editing: CronWorkspaceRow | null;
  onClose: () => void;
  onSuccess: () => void;
}

export interface CronWorkspaceAudience {
  title: string; // "Cron Jobs" (tenant) / "Cron Jobs (all tenants)" (admin)
  showCount: boolean; // admin shows the job count next to New
  query: { key: unknown[]; fn: () => Promise<{ items: CronWorkspaceRow[] }> }; // list operation
  searchPlaceholder: string; // i18n key
  searchOwner: boolean; // include the owner username in the search haystack (admin)
  // Full column set, in this screen's own order, built from the shared context
  // (use cronEnabledColumn / cronActionsColumn for the shared columns).
  buildColumns: (ctx: CronColumnContext) => TableColumnsType<CronWorkspaceRow>;
  pagination: TablePaginationConfig | false; // preserved explicitly per audience
  tableSize?: "small" | "middle" | "large";
  renderEditor: (p: CronEditorRenderProps) => ReactNode; // distinct admin/tenant editors
  renderEmpty?: (openCreate: () => void) => ReactNode; // admin EmptyWithCTA; tenant default
}

export function CronJobWorkspace({ audience }: { audience: CronWorkspaceAudience }) {
  const { t } = useTranslation();
  const { message: antMessage } = App.useApp();

  const [search, setSearch] = useState("");
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [runningId, setRunningId] = useState<string | null>(null);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [logJobId, setLogJobId] = useState<string | null>(null);
  const [logOpen, setLogOpen] = useState(false);
  const [runResult, setRunResult] = useState<CronRunNowResponse | null>(null);
  const [runOpen, setRunOpen] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<CronWorkspaceRow | null>(null);

  const {
    data: listResponse = { items: [] },
    isLoading,
    refetch,
  } = useQuery({ queryKey: audience.query.key, queryFn: audience.query.fn });

  const jobs = useMemo(() => listResponse.items || [], [listResponse]);
  const filteredJobs = useMemo(() => {
    if (!search) return jobs;
    const needle = search.toLowerCase();
    return jobs.filter(
      (j) =>
        j.name.toLowerCase().includes(needle) ||
        j.command.toLowerCase().includes(needle) ||
        j.schedule.toLowerCase().includes(needle) ||
        (audience.searchOwner && (j.username || "").toLowerCase().includes(needle)),
    );
  }, [jobs, search, audience.searchOwner]);

  const surfaceErr = (err: unknown, fallback: string) => {
    const msg =
      (err as { response?: { data?: { detail?: string } }; message?: string })?.response?.data?.detail ??
      (err as { message?: string })?.message ??
      fallback;
    antMessage.error(msg);
  };

  const handleDelete = async (job: CronWorkspaceRow) => {
    setDeletingId(job.id);
    try {
      await deleteCronJob(job.id);
      antMessage.success("Cron job deleted");
      refetch();
    } catch (e) {
      surfaceErr(e, "Failed to delete cron job");
    } finally {
      setDeletingId(null);
    }
  };

  const handleRun = async (job: CronWorkspaceRow) => {
    setRunningId(job.id);
    try {
      const result = await runCronJobNow(job.id);
      setRunResult(result);
      setRunOpen(true);
      // Refetch to pick up the new last_run_at / last_exit_code once the run lands.
      setTimeout(() => refetch(), 2000);
    } catch (e) {
      surfaceErr(e, "Failed to run cron job");
    } finally {
      setRunningId(null);
    }
  };

  const handleToggle = async (job: CronWorkspaceRow) => {
    setTogglingId(job.id);
    try {
      await updateCronJob(job.id, { enabled: !job.enabled });
      antMessage.success(job.enabled ? "Cron job disabled" : "Cron job enabled");
      refetch();
    } catch (e) {
      surfaceErr(e, "Failed to update cron job");
    } finally {
      setTogglingId(null);
    }
  };

  const openLog = (job: CronWorkspaceRow) => {
    setLogJobId(job.id);
    setLogOpen(true);
  };

  const openCreate = () => {
    setEditing(null);
    setEditorOpen(true);
  };
  const openEdit = (job: CronWorkspaceRow) => {
    setEditing(job);
    setEditorOpen(true);
  };
  const closeEditor = () => {
    setEditorOpen(false);
    setEditing(null);
  };

  const columns = audience.buildColumns({
    busy: { deletingId, runningId, togglingId },
    onToggle: handleToggle,
    onRun: handleRun,
    onLog: openLog,
    onEdit: openEdit,
    onDelete: handleDelete,
  });

  return (
    <div>
      <Space wrap align="center" style={{ marginBottom: 16, width: "100%", justifyContent: "space-between" }}>
        <Typography.Title level={3} style={{ margin: 0 }}>
          <CalendarCheckOutlined /> {audience.title}
        </Typography.Title>
        <Space>
          {audience.showCount && (
            <Typography.Text type="secondary">
              {jobs.length} job{jobs.length === 1 ? "" : "s"}
            </Typography.Text>
          )}
          <Button type="primary" icon={<PlusSquareOutlined />} onClick={openCreate}>
            New Cron Job
          </Button>
        </Space>
      </Space>

      <Card>
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Input.Search
            placeholder={t(audience.searchPlaceholder)}
            allowClear
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onSearch={(value) => setSearch(value.trim())}
            style={{ maxWidth: 480 }}
          />
          <Table<CronWorkspaceRow>
            rowKey="id"
            size={audience.tableSize}
            loading={isLoading || deletingId !== null}
            dataSource={filteredJobs}
            pagination={audience.pagination}
            scroll={{ x: "max-content" }}
            locale={audience.renderEmpty ? { emptyText: audience.renderEmpty(openCreate) } : undefined}
            columns={columns}
          />
        </Space>
      </Card>

      {audience.renderEditor({
        open: editorOpen,
        editing,
        onClose: closeEditor,
        onSuccess: () => {
          closeEditor();
          refetch();
        },
      })}

      {logJobId && (
        <CronLogDrawer
          open={logOpen}
          jobId={logJobId}
          onClose={() => {
            setLogOpen(false);
            setLogJobId(null);
          }}
        />
      )}
      <RunNowResultModal
        open={runOpen}
        result={runResult}
        onClose={() => {
          setRunOpen(false);
          setRunResult(null);
        }}
      />
    </div>
  );
}

// DashboardTabs — the admin Dashboard's tabbed main surface (GH #1249, the
// Option 6 tabs carrying the Option 3 inventory). Each tab's pane owns its
// queries and is mounted only while its tab is open, so landing on the
// Dashboard fetches the Activity rows and nothing else; the other lists load
// on first click (#981: an idle Dashboard must not keep the panel busy). The
// open tab lives in ?tab= so a link (the health tag) can open Alerts.
import type { ReactNode } from "react";
import { Badge, Card, Col, Row, Table, Tag, Tooltip, Typography } from "antd";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";

import { GlobalOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import {
  AuditActionLabel,
  type AuditRow,
  fmtTSInTz,
  resultTag,
  useServerTz,
} from "../../../components/AuditEventDetail";
import { BackupStatusTag } from "../../../components/backups/BackupStatusTag";
import {
  RunStatusSummary,
  type RunStatusCounts,
} from "../../../components/backups/RunStatusSummary";
import { useListQuery } from "../../../hooks/useQueries";
import type { Alert as HostAlert } from "../../../hooks/useServerStatus";
import { backupTypeColor, backupTypeLabel, runScopeSummary } from "../../../utils/backupType";
import { shortDateTime } from "../../../utils/datetime";

dayjs.extend(relativeTime);

const TAB_KEYS = ["activity", "inventory", "packages", "backups", "alerts"] as const;
type TabKey = (typeof TAB_KEYS)[number];

const RECENT_LIMIT = 5;
const ACTIVITY_LIMIT = 8;
const BACKUP_LIMIT = 6;

interface Props {
  alerts: HostAlert[];
}

export function DashboardTabs({ alerts }: Props) {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const raw = params.get("tab");
  const active: TabKey = TAB_KEYS.includes(raw as TabKey) ? (raw as TabKey) : "activity";

  const onChange = (key: string) => {
    const next = new URLSearchParams(params);
    if (key === "activity") next.delete("tab");
    else next.set("tab", key);
    setParams(next, { replace: true });
  };

  const critical = alerts.some((a) => a.level === "critical");
  const labels: Record<TabKey, ReactNode> = {
    activity: t("dashboard.tabs.activity"),
    inventory: t("dashboard.tabs.inventory"),
    packages: t("dashboard.tabs.packages"),
    backups: t("dashboard.tabs.backups"),
    alerts: (
      <span>
        {t("dashboard.tabs.alerts")}{" "}
        <Badge
          count={alerts.length}
          size="small"
          color={critical ? "#cf1322" : "#fa8c16"}
          title={t("dashboard.alerts.count", { count: alerts.length })}
        />
      </span>
    ),
  };

  return (
    <Card
      size="small"
      activeTabKey={active}
      onTabChange={onChange}
      // nowrap: on a phone a long label wrapped onto three lines; unwrapped,
      // the tabs that do not fit move into the tab bar's overflow menu.
      tabList={TAB_KEYS.map((key) => ({
        key,
        label: <span style={{ whiteSpace: "nowrap" }}>{labels[key]}</span>,
      }))}
    >
      {active === "activity" && <ActivityPane />}
      {active === "inventory" && <InventoryPane />}
      {active === "packages" && <PackagesPane />}
      {active === "backups" && <BackupsPane />}
      {active === "alerts" && <AlertsPane alerts={alerts} />}
    </Card>
  );
}

function relative(ts: string | undefined) {
  if (!ts) return "—";
  const d = dayjs(ts);
  return d.isValid() ? d.fromNow() : ts;
}

function PaneFooter({ to, label }: { to: string; label: string }) {
  return (
    <div style={{ textAlign: "right", marginTop: 8 }}>
      <Link to={to}>{label}</Link>
    </div>
  );
}

// ActivityPane — the newest audit rows, the same store as the Audit Log page.
function ActivityPane() {
  const { t } = useTranslation();
  const tz = useServerTz();
  const q = useListQuery<AuditRow>({
    resource: "admin/audit",
    params: { pageSize: ACTIVITY_LIMIT, sort: "ts", order: "desc" },
  });
  return (
    <>
      <Table<AuditRow>
        size="small"
        rowKey="id"
        pagination={false}
        showHeader={false}
        loading={q.isLoading}
        dataSource={q.items}
        scroll={{ x: "max-content" }}
        locale={{ emptyText: t("dashboard.activity.empty") }}
        columns={[
          {
            key: "actor",
            render: (_: unknown, r: AuditRow) => (
              <Typography.Text strong>{r.actor_name || r.actor_kind}</Typography.Text>
            ),
          },
          {
            key: "action",
            render: (_: unknown, r: AuditRow) => <AuditActionLabel row={r} />,
          },
          {
            key: "target",
            render: (_: unknown, r: AuditRow) =>
              r.target_type || r.target_id ? (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {r.target_type}
                  {r.target_id ? ` ${r.target_id}` : ""}
                </Typography.Text>
              ) : null,
          },
          {
            key: "result",
            render: (_: unknown, r: AuditRow) => (r.result === "ok" ? null : resultTag(r.result)),
          },
          {
            key: "ts",
            align: "right" as const,
            render: (_: unknown, r: AuditRow) => (
              <Tooltip title={`${fmtTSInTz(r.ts, tz)} (${tz})`}>
                <Typography.Text type="secondary" style={{ fontSize: 12, whiteSpace: "nowrap" }}>
                  {relative(r.ts)}
                </Typography.Text>
              </Tooltip>
            ),
          },
        ]}
      />
      <PaneFooter to="/jabali-admin/audit" label={t("dashboard.activity.open")} />
    </>
  );
}

interface UserRow {
  id: string;
  email: string;
  username?: string | null;
  is_admin?: boolean;
}
interface DomainRow {
  id: string;
  name: string;
}
interface DatabaseRow {
  id: string;
  name: string;
  engine?: string;
}

// InventoryPane — the newest users, web domains and databases side by side.
function InventoryPane() {
  const { t } = useTranslation();
  const recent = { pageSize: RECENT_LIMIT, sort: "created_at", order: "desc" as const };
  const users = useListQuery<UserRow>({ resource: "users", params: recent });
  const domains = useListQuery<DomainRow>({ resource: "domains", params: recent });
  const databases = useListQuery<DatabaseRow>({ resource: "databases", params: recent });

  const section = (title: string, to: string, table: ReactNode) => (
    <Col xs={24} xl={8}>
      <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 8 }}>
        <Typography.Text strong>{title}</Typography.Text>
        <Link to={to}>{t("dashboard.view_all")}</Link>
      </div>
      {table}
    </Col>
  );

  return (
    <Row gutter={[16, 16]}>
      {section(
        t("dashboard.recent_users"),
        "/jabali-admin/users",
        <Table<UserRow>
          size="small"
          rowKey="id"
          pagination={false}
          showHeader={false}
          loading={users.isLoading}
          dataSource={users.items}
          scroll={{ x: "max-content" }}
          locale={{ emptyText: t("dashboard.empty.users") }}
          columns={[
            { key: "name", ellipsis: true, render: (_: unknown, r: UserRow) => r.username || r.email },
            {
              key: "role",
              width: 70,
              render: (_: unknown, r: UserRow) =>
                r.is_admin ? <Tag color="blue">{t("dashboard.role.admin")}</Tag> : <Tag>{t("dashboard.role.user")}</Tag>,
            },
          ]}
        />,
      )}
      {section(
        t("dashboard.recent_domains"),
        "/jabali-admin/domains",
        <Table<DomainRow>
          size="small"
          rowKey="id"
          pagination={false}
          showHeader={false}
          loading={domains.isLoading}
          dataSource={domains.items}
          scroll={{ x: "max-content" }}
          locale={{ emptyText: t("dashboard.empty.domains") }}
          columns={[
            {
              key: "name",
              ellipsis: true,
              render: (_: unknown, r: DomainRow) => (
                <span>
                  <GlobalOutlined style={{ marginInlineEnd: 8, color: "#1677ff" }} />
                  {r.name}
                </span>
              ),
            },
          ]}
        />,
      )}
      {section(
        t("dashboard.recent_databases"),
        "/jabali-admin/databases",
        <Table<DatabaseRow>
          size="small"
          rowKey="id"
          pagination={false}
          showHeader={false}
          loading={databases.isLoading}
          dataSource={databases.items}
          scroll={{ x: "max-content" }}
          locale={{ emptyText: t("dashboard.empty.databases") }}
          columns={[
            { key: "name", ellipsis: true, render: (_: unknown, r: DatabaseRow) => r.name },
            {
              key: "engine",
              width: 90,
              render: (_: unknown, r: DatabaseRow) =>
                r.engine ? <Tag color={r.engine === "postgres" ? "green" : "blue"}>{r.engine}</Tag> : null,
            },
          ]}
        />,
      )}
    </Row>
  );
}

interface PackageRow {
  id: string;
  name: string;
  ssh_enabled?: boolean;
}

function PackagesPane() {
  const { t } = useTranslation();
  const q = useListQuery<PackageRow>({
    resource: "packages",
    params: { pageSize: 10, sort: "name", order: "asc" },
  });
  return (
    <>
      <Table<PackageRow>
        size="small"
        rowKey="id"
        pagination={false}
        loading={q.isLoading}
        dataSource={q.items}
        scroll={{ x: "max-content" }}
        locale={{ emptyText: t("dashboard.empty.packages") }}
        columns={[
          { title: t("dashboard.col.name"), dataIndex: "name", ellipsis: true },
          {
            title: t("dashboard.col.ssh"),
            dataIndex: "ssh_enabled",
            width: 80,
            render: (v: boolean) =>
              v ? <Tag color="green">{t("dashboard.ssh.on")}</Tag> : <Tag>{t("dashboard.ssh.off")}</Tag>,
          },
        ]}
      />
      <PaneFooter to="/jabali-admin/packages" label={t("dashboard.packages_open")} />
    </>
  );
}

interface BackupRun extends RunStatusCounts {
  run_id: string;
  kind: string;
  content?: string;
  has_accounts?: boolean;
  accounts?: number;
  started_at: string;
}
interface BackupJob {
  id: string;
  kind: string;
  content?: string;
  status: string;
  created_at: string;
}
interface RunsEnvelope {
  data?: BackupRun[];
  manual?: BackupJob[];
}
type BackupRow =
  | { key: string; at: string; run: BackupRun; job?: undefined }
  | { key: string; at: string; job: BackupJob; run?: undefined };

// BackupsPane — the newest backup runs and manual jobs, newest first, from
// the same /admin/backup-runs list the Backups page shows.
function BackupsPane() {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["dashboard", "backup-runs", BACKUP_LIMIT],
    queryFn: async () => {
      const r = await apiClient.get<RunsEnvelope>(`/admin/backup-runs?page_size=${BACKUP_LIMIT}`);
      return r.data;
    },
  });
  const rows: BackupRow[] = [
    ...(q.data?.data ?? []).map((run) => ({ key: `run-${run.run_id}`, at: run.started_at, run })),
    ...(q.data?.manual ?? []).map((job) => ({ key: `job-${job.id}`, at: job.created_at, job })),
  ]
    .sort((a, b) => +new Date(b.at) - +new Date(a.at))
    .slice(0, BACKUP_LIMIT);

  return (
    <>
      <Table<BackupRow>
        size="small"
        rowKey="key"
        pagination={false}
        showHeader={false}
        loading={q.isLoading}
        dataSource={rows}
        scroll={{ x: "max-content" }}
        locale={{ emptyText: t("dashboard.backups.empty") }}
        columns={[
          {
            key: "type",
            render: (_: unknown, r: BackupRow) => {
              const kind = r.run ? r.run.kind : r.job.kind;
              const content = r.run ? r.run.content : r.job.content;
              const scope = r.run ? runScopeSummary(r.run.total, r.run.accounts ?? 0) : "";
              return (
                <span>
                  <Tag color={backupTypeColor(kind, content)}>
                    {backupTypeLabel(kind, content, r.run?.has_accounts ?? false)}
                  </Tag>
                  {scope && (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {scope}
                    </Typography.Text>
                  )}
                </span>
              );
            },
          },
          {
            key: "status",
            render: (_: unknown, r: BackupRow) =>
              r.run ? <RunStatusSummary run={r.run} /> : <BackupStatusTag status={r.job.status} />,
          },
          {
            key: "at",
            align: "right" as const,
            render: (_: unknown, r: BackupRow) => (
              <Tooltip title={shortDateTime(r.at)}>
                <Typography.Text type="secondary" style={{ fontSize: 12, whiteSpace: "nowrap" }}>
                  {relative(r.at)}
                </Typography.Text>
              </Tooltip>
            ),
          },
        ]}
      />
      <PaneFooter to="/jabali-admin/backups" label={t("dashboard.backups.open")} />
    </>
  );
}

// AlertsPane — the host alerts Server Status raises, from the envelope the
// Dashboard already polls, so this tab costs no request of its own.
function AlertsPane({ alerts }: { alerts: HostAlert[] }) {
  const { t } = useTranslation();
  const sorted = [...alerts].sort(
    (a, b) => Number(b.level === "critical") - Number(a.level === "critical"),
  );
  return (
    <>
      <Table<HostAlert>
        size="small"
        rowKey={(a) => `${a.level}:${a.kind}:${a.detail}`}
        pagination={false}
        showHeader={false}
        dataSource={sorted}
        scroll={{ x: "max-content" }}
        locale={{ emptyText: t("dashboard.alerts.empty") }}
        columns={[
          {
            key: "level",
            width: 90,
            render: (_: unknown, a: HostAlert) =>
              a.level === "critical" ? (
                <Tag color="red">{t("dashboard.alerts.critical")}</Tag>
              ) : (
                <Tag color="orange">{t("dashboard.alerts.warning")}</Tag>
              ),
          },
          { key: "detail", render: (_: unknown, a: HostAlert) => a.detail || a.kind },
        ]}
      />
      <PaneFooter to="/jabali-admin/server-status" label={t("dashboard.alerts.open")} />
    </>
  );
}

// DockerAppInventory — the shared Installed-app inventory body for the admin and
// tenant Docker Apps pages (JAB-335). The two pages duplicated the installed
// stats, search, table, polling, and the toggle/delete/logs/credentials
// orchestration; the tenant's restrictions (loopback-only ports, no
// exec/edit/update/rebuild/backups) were expressed by role inference rather than
// an explicit contract. This Module owns the shared behavior with ONE
// implementation; a capability audience supplies the list operation, cache key,
// port presentation, the shared verbs, and — only for the admin — the privileged
// verbs. The privileged verbs are absent from the tenant audience, so the tenant
// UI cannot render or dispatch them by construction.
//
// Scope is the Installed tab body only. The two shells keep their tabs, the
// catalog grid + install flow, owner filter / breadcrumbs (admin), the
// not-enabled and over-quota notices and maintenance tab, and the log /
// credentials overlays (which differ in capability: admin edits env, tenant
// reads it) — the Module dispatches those through onLogs / onCredentials.
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { App, Button, Col, Empty, Input, Row, Table, Typography } from "antd";
import {
  AppstoreOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  SyncOutlined,
} from "@icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { StatCard } from "../StatCard";
import type { RowAction } from "../RowActions";
import type { InstalledApp } from "../../shells/admin/docker-apps/types";
import {
  dockerInstalledColumns,
  type DockerRemovePolicy,
  type PortPresentation,
} from "./dockerColumns";
import { TRANSITIONAL_STATUSES } from "./dockerStatus";

export interface DockerInventoryLabels {
  installedApps: string; // i18n key
  updatesAvailable: string;
  running: string;
  stopped: string;
  searchPlaceholder: string;
  noAppsYet: string;
}

export interface DockerInventoryAudience {
  // list operation + the SINGLE cache key used for the query and every
  // invalidation (AC5) — admin ["docker-apps-installed"], tenant ["user-docker-installed"].
  installedKey: unknown[];
  listInstalled: () => Promise<InstalledApp[]>;
  catalogCount: number; // from the shell's own catalog query (no second fetch here)
  catalogIconUrl: (slug: string) => string;
  portPresentation: PortPresentation;
  // Tenant-safe lifecycle union. Admin's wider fn (which also accepts "rebuild")
  // is assignable here; the Module can only ever dispatch start/stop/restart.
  lifecycle: (id: string, action: "start" | "stop" | "restart") => Promise<void>;
  remove: DockerRemovePolicy & { successMessage: string; fn: (id: string) => Promise<void> };
  onLogs: (app: InstalledApp) => void;
  onCredentials: (app: InstalledApp) => void;
  // Admin-only privileged verbs; tenant omits the field entirely.
  privilegedActions?: (app: InstalledApp) => RowAction[];
  // Admin scopes the list to a selected owner; tenant sees only its own.
  rowFilter?: (app: InstalledApp) => boolean;
  labels: DockerInventoryLabels;
  onBrowseCatalog: () => void;
}

export function DockerAppInventory({ audience }: { audience: DockerInventoryAudience }) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const [search, setSearch] = useState("");

  const installed = useQuery({
    queryKey: audience.installedKey,
    queryFn: audience.listInstalled,
    // One polling policy: refetch every 8s only while a row is mid-transition,
    // then stop. (Admin previously polled unconditionally; the tenant missed
    // pending/rolling_back.)
    refetchInterval: (q) =>
      (q.state.data ?? []).some((a) => TRANSITIONAL_STATUSES.has(a.status)) ? 8000 : false,
  });

  const surfaceErr = (err: unknown, fallback: string) => {
    const msg =
      (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail ??
      (err as { message?: string })?.message ??
      fallback;
    message.error(msg);
  };

  const life = useMutation({
    mutationFn: ({ id, action }: { id: string; action: "start" | "stop" | "restart" }) =>
      audience.lifecycle(id, action),
    onSuccess: () => qc.invalidateQueries({ queryKey: audience.installedKey }),
    onError: (e: unknown) => surfaceErr(e, "Action failed"),
  });

  const del = useMutation({
    mutationFn: (id: string) => audience.remove.fn(id),
    onSuccess: () => {
      message.success(audience.remove.successMessage);
      qc.invalidateQueries({ queryKey: audience.installedKey });
    },
    onError: (e: unknown) => surfaceErr(e, "Delete failed"),
  });

  const allRows = useMemo(() => installed.data ?? [], [installed.data]);
  const scopedRows = useMemo(
    () => (audience.rowFilter ? allRows.filter(audience.rowFilter) : allRows),
    [allRows, audience],
  );
  const filteredRows = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return scopedRows;
    return scopedRows.filter(
      (r) =>
        r.name.toLowerCase().includes(q) ||
        r.slug.toLowerCase().includes(q) ||
        (r.domain ?? "").toLowerCase().includes(q),
    );
  }, [scopedRows, search]);

  const installedCount = scopedRows.length;
  const runningCount = scopedRows.filter((r) => r.status === "running").length;
  const stoppedCount = scopedRows.filter((r) => r.status === "stopped").length;
  const updateCount = scopedRows.filter(
    (r) => r.available_digest && r.available_digest !== (r.image_sha ?? ""),
  ).length;
  const pct = (n: number) => (installedCount > 0 ? Math.round((n / installedCount) * 100) : 0);

  const columns = dockerInstalledColumns({
    catalogIconUrl: audience.catalogIconUrl,
    portPresentation: audience.portPresentation,
    onLifecycle: (app, action) => life.mutate({ id: app.id, action }),
    onLogs: audience.onLogs,
    onCredentials: audience.onCredentials,
    onDelete: (app) => del.mutate(app.id),
    remove: audience.remove,
    privilegedActions: audience.privilegedActions,
  });

  return (
    <div>
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={24} sm={12} lg={6}>
          <StatCard
            iconBg="rgba(207, 19, 34, 0.12)" iconColor="#cf1322" Icon={AppstoreOutlined}
            label={t(audience.labels.installedApps)} value={installedCount}
            subtitle={<Typography.Text type="secondary">{audience.catalogCount} in catalog</Typography.Text>}
          />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatCard
            iconBg="rgba(114, 46, 209, 0.12)" iconColor="#722ed1" Icon={SyncOutlined}
            label={t(audience.labels.updatesAvailable)} value={updateCount}
            subtitle={updateCount > 0
              ? <Typography.Text type="warning">Needs attention</Typography.Text>
              : <Typography.Text type="secondary">Up to date</Typography.Text>}
          />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatCard
            iconBg="rgba(63, 134, 0, 0.12)" iconColor="#3f8600" Icon={PlayCircleOutlined}
            label={t(audience.labels.running)} value={runningCount}
            subtitle={<Typography.Text type="secondary">{pct(runningCount)}% of installed</Typography.Text>}
          />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatCard
            iconBg="rgba(212, 107, 8, 0.12)" iconColor="#d46b08" Icon={PauseCircleOutlined}
            label={t(audience.labels.stopped)} value={stoppedCount}
            subtitle={<Typography.Text type="secondary">{pct(stoppedCount)}% of installed</Typography.Text>}
          />
        </Col>
      </Row>

      <Input.Search
        allowClear
        placeholder={t(audience.labels.searchPlaceholder)}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        style={{ marginBottom: 16, maxWidth: 360 }}
      />

      <Table<InstalledApp>
        rowKey="id"
        size="small"
        loading={installed.isLoading}
        dataSource={filteredRows}
        scroll={{ x: 1100 }}
        pagination={{
          defaultPageSize: 25,
          showTotal: (total, range) => `Showing ${range[0]} to ${range[1]} of ${total} results`,
        }}
        locale={{
          emptyText: (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t(audience.labels.noAppsYet)}>
              <Button type="primary" onClick={audience.onBrowseCatalog}>
                Browse Catalog
              </Button>
            </Empty>
          ),
        }}
        columns={columns}
      />
    </div>
  );
}

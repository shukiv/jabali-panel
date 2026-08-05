// AdminDockerAppsPage — landing page for the M48 marketplace.
// Two tabs: Catalog (browse + install) and Installed (lifecycle).
import { useTranslation } from "react-i18next";
import { Alert, App, Avatar, Button, Col, Drawer, Empty, Input, Modal, Row, Space, Table, Tabs, Tag, Tooltip, Typography } from "antd";
import { useTabParam } from "../../../hooks/useTabParam";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { humanBytes } from "../../../utils/bytes";
import { useMemo, useState } from "react";
import {
  AppstoreOutlined,
  ContainerOutlined,
  ExportOutlined,
  PlayCircleOutlined,
  PauseCircleOutlined,
  ReloadOutlined,
  DeleteOutlined,
  SyncOutlined,
  FileTextOutlined,
  CodeOutlined,
  SaveOutlined,
  EditOutlined,
  KeyOutlined,
} from "@icons";
import { RowActions, type RowAction } from "../../../components/RowActions";

import { deleteApp, lifecycleAction, listCatalog, listInstalled, updateApp } from "./api";
import type { CatalogEntry, InstalledApp } from "./types";
import { useSearchParams } from "react-router";
import { useOneQuery } from "../../../hooks/useQueries";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { ownerResourceCrumbs, ownerLabel } from "../../../components/admin/entityLinks";
import { InstallDrawer } from "./InstallDrawer";
import { CatalogCard, CatalogGrid, CategoryFilter } from "../../../components/catalog";
import { LogsDrawer } from "./LogsDrawer";
import { ExecDrawer } from "./ExecDrawer";
import { BackupsDrawer } from "./BackupsDrawer";
import { EditDrawer } from "./EditDrawer";
import { EnvSection } from "./EnvSection";
import { MaintenanceTab } from "./MaintenanceTab";
import { StatCard } from "../../../components/StatCard";
import { useThemeMode } from "../../../theme/ThemeModeContext";

const STATUS_COLOR: Record<string, string> = {
  pending: "default",
  installing: "blue",
  running: "green",
  stopped: "orange",
  failed: "red",
  updating: "blue",
  rolling_back: "purple",
  deleted: "default",
};

export const AdminDockerAppsPage = () => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const { mode } = useThemeMode();
  const qc = useQueryClient();
  const [installEntry, setInstallEntry] = useState<CatalogEntry | null>(null);
  const [catalogTags, setCatalogTags] = useState<string[]>([]);
  const [logsAppId, setLogsAppId] = useState<string | null>(null);
  const [execAppId, setExecAppId] = useState<string | null>(null);
  const [credsAppId, setCredsAppId] = useState<string | null>(null);
  const [editApp, setEditApp] = useState<InstalledApp | null>(null);
  const [activeTab, setActiveTab] = useTabParam<string>("installed");
  const [backupsAppId, setBackupsAppId] = useState<string | null>(null);
  const [installedSearch, setInstalledSearch] = useState("");
  const [searchParams, setSearchParams] = useSearchParams();
  const ownerId = searchParams.get("user_id") ?? undefined;
  const ownerQ = useOneQuery<{ id: string; username?: string | null }>({
    resource: "users",
    id: ownerId,
    enabled: !!ownerId,
  });
  const clearOwner = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("user_id");
    setSearchParams(next);
  };

  const catalog = useQuery({
    queryKey: ["docker-apps-catalog"],
    queryFn: listCatalog,
  });

  const allCatalogTags = useMemo(() => {
    const set = new Set<string>();
    for (const e of catalog.data ?? []) for (const t of e.tags ?? []) set.add(t);
    return Array.from(set).sort();
  }, [catalog.data]);
  const visibleCatalog = useMemo(() => {
    const items = catalog.data ?? [];
    if (catalogTags.length === 0) return items;
    return items.filter((e) => (e.tags ?? []).some((t) => catalogTags.includes(t)));
  }, [catalog.data, catalogTags]);
  const installed = useQuery({
    queryKey: ["docker-apps-installed"],
    queryFn: listInstalled,
    refetchInterval: 8000, // poll while installs are in flight
  });

  const lifecycle = useMutation({
    mutationFn: async ({ id, action }: { id: string; action: "start" | "stop" | "restart" | "rebuild" }) =>
      lifecycleAction(id, action),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["docker-apps-installed"] }),
    onError: (e: unknown) => message.error(e instanceof Error ? e.message : "Action failed"),
  });

  const updateImage = useMutation({
    mutationFn: async (id: string) => updateApp(id),
    onSuccess: () => {
      // Async: the server returned 202 (update started). The row shows the
      // "updating" spinner and the 8s poll flips it to running/failed when
      // the background pull + recreate finishes. The click handler already
      // toasted the right message (update vs already-current, GH #794).
      qc.invalidateQueries({ queryKey: ["docker-apps-installed"] });
    },
    onError: (e: unknown) => message.error(e instanceof Error ? e.message : "Update failed"),
  });

  const remove = useMutation({
    mutationFn: async (id: string) => deleteApp(id, false),
    onSuccess: () => {
      message.success("Uninstalled");
      qc.invalidateQueries({ queryKey: ["docker-apps-installed"] });
    },
    onError: (e: unknown) => message.error(e instanceof Error ? e.message : "Delete failed"),
  });

  const ownerRef = ownerId
    ? { id: ownerId, username: ownerQ.data?.username }
    : undefined;

  useSetBreadcrumbs(
    ownerRef ? ownerResourceCrumbs(ownerRef, { key: "docker-apps", label: "Docker Apps" }) : null,
  );

  return (
    <div>
      <Space wrap align="center" style={{ marginBottom: 16 }}>
        <Typography.Title level={3} style={{ margin: 0 }}>
          <ContainerOutlined /> Docker Apps
        </Typography.Title>
        {ownerRef && (
          <Tag closable onClose={clearOwner} color="blue">
            Owner: {ownerLabel(ownerRef)}
          </Tag>
        )}
      </Space>

      <Tabs
        activeKey={activeTab}
        onChange={setActiveTab}
        items={[
          {
            key: "installed",
            label: "Installed",
            children: (
              <div>
                {(() => {
                  const rows = (installed.data ?? []).filter((r) => !ownerId || r.user_id === ownerId);
                  const installedCount = rows.length;
                  const runningCount = rows.filter(r => r.status === "running").length;
                  const stoppedCount = rows.filter(r => r.status === "stopped").length;
                  const updateCount = rows.filter(r => r.available_digest && r.available_digest !== r.image_sha).length;
                  const catalogCount = (catalog.data ?? []).length;
                  const pct = (n: number) => installedCount > 0 ? Math.round((n / installedCount) * 100) : 0;
                  return (
                    <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
                      <Col xs={24} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(207, 19, 34, 0.12)" iconColor="#cf1322" Icon={AppstoreOutlined}
                          label={t("admindockerappspage.installed_apps")} value={installedCount}
                          subtitle={<Typography.Text type="secondary">{catalogCount} in catalog</Typography.Text>}
                        />
                      </Col>
                      <Col xs={24} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(114, 46, 209, 0.12)" iconColor="#722ed1" Icon={SyncOutlined}
                          label={t("admindockerappspage.updates_available")} value={updateCount}
                          subtitle={updateCount > 0
                            ? <Typography.Text type="warning">Needs attention</Typography.Text>
                            : <Typography.Text type="secondary">Up to date</Typography.Text>}
                        />
                      </Col>
                      <Col xs={24} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(63, 134, 0, 0.12)" iconColor="#3f8600" Icon={PlayCircleOutlined}
                          label={t("admindockerappspage.running")} value={runningCount}
                          subtitle={<Typography.Text type="secondary">{pct(runningCount)}% of installed</Typography.Text>}
                        />
                      </Col>
                      <Col xs={24} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(212, 107, 8, 0.12)" iconColor="#d46b08" Icon={PauseCircleOutlined}
                          label={t("admindockerappspage.stopped")} value={stoppedCount}
                          subtitle={<Typography.Text type="secondary">{pct(stoppedCount)}% of installed</Typography.Text>}
                        />
                      </Col>
                    </Row>
                  );
                })()}
              <Input.Search
                allowClear
                placeholder={t("admindockerappspage.search_by_name")}
                value={installedSearch}
                onChange={(e) => setInstalledSearch(e.target.value)}
                style={{ marginBottom: 16, maxWidth: 360 }}
              />
              <Table<InstalledApp>
                rowKey="id"
                size="small"
                loading={installed.isLoading}
                dataSource={(installed.data ?? []).filter((r) => {
                  if (ownerId && r.user_id !== ownerId) return false;
                  const q = installedSearch.trim().toLowerCase();
                  if (!q) return true;
                  return (
                    r.name.toLowerCase().includes(q) ||
                    r.slug.toLowerCase().includes(q) ||
                    (r.domain ?? "").toLowerCase().includes(q)
                  );
                })}
                scroll={{ x: 1100 }}
                pagination={{
                  pageSize: 25,
                  showTotal: (total, range) => `Showing ${range[0]} to ${range[1]} of ${total} results`,
                }}
                locale={{
                  emptyText: (
                    <Empty
                      image={Empty.PRESENTED_IMAGE_SIMPLE}
                      description={t("admindockerappspage.no_installed_apps_yet")}
                    >
                      <Button type="primary" onClick={() => setActiveTab("catalog")}>
                        Browse Catalog
                      </Button>
                    </Empty>
                  ),
                }}
                columns={[
                  {
                    title: "Name",
                    dataIndex: "name",
                    width: 260,
                    sorter: (a, b) => a.name.localeCompare(b.name),
                    defaultSortOrder: "ascend",
                    render: (n, r) => (
                      <Space size={12} align="start">
                        <Avatar
                          shape="square"
                          size={40}
                          src={`/api/v1/admin/docker-apps/catalog/${r.slug}/icon?theme=${mode}`}
                          style={{ background: "rgba(255,255,255,0.04)" }}
                        >
                          {r.slug.slice(0, 2).toUpperCase()}
                        </Avatar>
                        <Space direction="vertical" size={0}>
                          <Typography.Text strong>{n}</Typography.Text>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                            {r.slug} @ {r.catalog_version}
                          </Typography.Text>
                          {r.domain && (
                            <Typography.Link
                              href={`https://${r.domain}/`}
                              target="_blank"
                              rel="noopener noreferrer"
                              style={{ fontSize: 12 }}
                            >
                              {r.domain}
                            </Typography.Link>
                          )}
                          {r.post_install_note && (
                            <Alert
                              type="info"
                              showIcon
                              style={{ marginTop: 6, padding: "4px 8px", maxWidth: 320 }}
                              message={
                                <Typography.Text style={{ fontSize: 11, whiteSpace: "normal" }}>
                                  {r.post_install_note}
                                </Typography.Text>
                              }
                            />
                          )}
                        </Space>
                      </Space>
                    ),
                  },
                  {
                    title: "Status",
                    dataIndex: "status",
                    width: 180,
                    sorter: (a, b) => a.status.localeCompare(b.status),
                    render: (s, r) => (
                      <Space direction="vertical" size={4}>
                        <Space size={4}>
                          <Tag color={STATUS_COLOR[s] || "default"}>{s}</Tag>
                          {(s === "installing" || s === "updating" || s === "rolling_back") && <SyncOutlined spin />}
                          {r.last_error && (
                            <Tooltip title={r.last_error}>
                              <Tag color="red">err</Tag>
                            </Tooltip>
                          )}
                        </Space>
                        {r.available_digest && r.available_digest !== (r.image_sha ?? "") && (
                          <Tooltip title={`Upstream digest moved to ${r.available_digest.slice(0, 19)}...`}>
                            <Tag color="purple">update available</Tag>
                          </Tooltip>
                        )}
                      </Space>
                    ),
                  },
                  {
                    title: "Ports",
                    dataIndex: "ports",
                    width: 320,
                    render: (_, r) => (
                      <Space direction="vertical" size={4} style={{ width: "100%" }}>
                        {(r.ports ?? []).map((p) => {
                          const bindLabel = p.bind_interface === "loopback" || p.bind_interface === "127.0.0.1" ? "loopback" : "public";
                          const linkHost = p.bind_interface === "loopback" || p.bind_interface === "127.0.0.1" ? "127.0.0.1" : window.location.hostname;
                          const proto = p.protocol === "tcp" ? "http" : p.protocol;
                          const href = `${proto === "http" ? "http" : "https"}://${linkHost}:${p.host_port}`;
                          return (
                            <Space key={p.id} size={6} style={{ width: "100%" }}>
                              <Tag style={{ margin: 0, minWidth: 48, textAlign: "center" }}>{p.port_name}</Tag>
                              <Tag style={{ margin: 0 }}>
                                <Space size={4}>
                                  <span>{bindLabel}:{p.host_port}/{p.protocol}</span>
                                  <a href={href} target="_blank" rel="noopener noreferrer" style={{ color: "inherit", display: "inline-flex" }}>
                                    <ExportOutlined style={{ fontSize: 12 }} />
                                  </a>
                                </Space>
                              </Tag>
                            </Space>
                          );
                        })}
                      </Space>
                    ),
                  },
                  {
                    title: "Limits",
                    width: 280,
                    render: (_, r) => (
                      <Space size={16} wrap>
                        <span>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>CPU: </Typography.Text>
                          <Typography.Text strong style={{ fontSize: 12 }}>{r.cpu_limit ?? "—"}</Typography.Text>
                        </span>
                        <span>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>Memory: </Typography.Text>
                          <Typography.Text strong style={{ fontSize: 12 }}>{r.memory_limit ?? "—"}</Typography.Text>
                        </span>
                        <span>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>PIDs: </Typography.Text>
                          <Typography.Text strong style={{ fontSize: 12 }}>{r.pids_limit ?? "—"}</Typography.Text>
                        </span>
                      </Space>
                    ),
                  },
                  {
                    title: "Disk",
                    dataIndex: "data_bytes",
                    width: 110,
                    sorter: (a, b) => (a.data_bytes ?? 0) - (b.data_bytes ?? 0),
                    render: (_, r) => (
                      <Tooltip
                        title={
                          r.size_checked_at
                            ? `Persistent data on disk · checked ${new Date(r.size_checked_at).toLocaleString()}`
                            : "Persistent data size — not measured yet"
                        }
                      >
                        <Typography.Text strong style={{ fontSize: 12 }}>
                          {r.data_bytes > 0 ? humanBytes(r.data_bytes) : "—"}
                        </Typography.Text>
                      </Tooltip>
                    ),
                  },
                  {
                    title: "Actions",
                    width: 180,
                    align: "right" as const,
                    render: (_, r) => {
                      const running = r.status === "running";
                      const confirmDelete = () =>
                        Modal.confirm({
                          title: `Uninstall ${r.name}?`,
                          content: "Volumes will be purged. This cannot be undone.",
                          okText: "Uninstall",
                          okButtonProps: { danger: true },
                          onOk: () => remove.mutate(r.id),
                        });
                      const actions: RowAction[] = [
                        running
                          ? { key: "stop", label: "Stop", icon: <PauseCircleOutlined />, onClick: () => lifecycle.mutate({ id: r.id, action: "stop" }) }
                          : { key: "start", label: "Start", icon: <PlayCircleOutlined />, onClick: () => lifecycle.mutate({ id: r.id, action: "start" }) },
                        { key: "restart", label: "Restart", icon: <ReloadOutlined />, onClick: () => lifecycle.mutate({ id: r.id, action: "restart" }) },
                        { key: "edit", label: "Edit", icon: <EditOutlined />, disabled: running, onClick: () => setEditApp(r) },
                        {
                          key: "update",
                          label: "Update",
                          icon: <SyncOutlined />,
                          // GH #794: catalog images are pinned to a reviewed
                          // digest, so a newer version only appears when the
                          // catalog is bumped. Tell the operator up-front why
                          // "Update" won't change the version when they're
                          // already current (it still re-applies catalog config).
                          onClick: () => {
                            const hasUpdate = !!r.available_digest && r.available_digest !== (r.image_sha ?? "");
                            message.info(
                              hasUpdate
                                ? "Update started — this can take a few minutes"
                                : "Already on the latest catalog version — re-applying catalog config, but there's no newer version to pull yet. New app versions arrive when the catalog is bumped.",
                            );
                            updateImage.mutate(r.id);
                          },
                        },
                        { key: "logs", label: "Logs", icon: <FileTextOutlined />, onClick: () => setLogsAppId(r.id) },
                        { key: "creds", label: "Credentials", icon: <KeyOutlined />, onClick: () => setCredsAppId(r.id) },
                        { key: "exec", label: "Exec", icon: <CodeOutlined />, onClick: () => setExecAppId(r.id) },
                        { key: "backups", label: "Backups", icon: <SaveOutlined />, onClick: () => setBackupsAppId(r.id) },
                        { key: "delete", label: "Uninstall", icon: <DeleteOutlined />, danger: true, onClick: confirmDelete },
                      ];
                      return <RowActions actions={actions} />;
                    },
                  },
                ]}
              />
              </div>
            ),
          },
          {
            key: "catalog",
            label: "Catalog",
            children: (
              <>
              <CategoryFilter
                tags={allCatalogTags}
                selected={catalogTags}
                onChange={setCatalogTags}
              />
              <CatalogGrid>
                {visibleCatalog.map((e) => (
                  <CatalogCard
                    key={e.slug}
                    name={e.name}
                    iconUrl={`/api/v1/admin/docker-apps/catalog/${e.slug}/icon?theme=${mode}`}
                    meta={<Tag style={{ marginInlineEnd: 0 }}>v{e.version}</Tag>}
                    description={e.description}
                    onInstall={() => setInstallEntry(e)}
                  />
                ))}
              </CatalogGrid>
              </>
            ),
          },
          {
            key: "maintenance",
            label: "Maintenance",
            children: <MaintenanceTab />,
          }

        ]}
      />

      <InstallDrawer
        open={installEntry !== null}
        entry={installEntry}
        onClose={() => setInstallEntry(null)}
        onInstalled={() => setActiveTab("installed")}
      />
      <LogsDrawer
        open={logsAppId !== null}
        appId={logsAppId}
        appName={(installed.data ?? []).find((a) => a.id === logsAppId)?.name}
        lastError={(installed.data ?? []).find((a) => a.id === logsAppId)?.last_error}
        onClose={() => setLogsAppId(null)}
      />
      <ExecDrawer
        open={execAppId !== null}
        appId={execAppId}
        onClose={() => setExecAppId(null)}
      />
      <EditDrawer
        open={editApp !== null}
        app={editApp}
        onClose={() => setEditApp(null)}
      />
      <BackupsDrawer
        open={backupsAppId !== null}
        appId={backupsAppId}
        onClose={() => setBackupsAppId(null)}
      />
      <Drawer
        title={t("admindockerappspage.credentials")}
        open={credsAppId !== null}
        onClose={() => setCredsAppId(null)}
        width={760}
        destroyOnClose
      >
        {credsAppId && <EnvSection appId={credsAppId} active={credsAppId !== null} />}
      </Drawer>
    </div>
  );
};

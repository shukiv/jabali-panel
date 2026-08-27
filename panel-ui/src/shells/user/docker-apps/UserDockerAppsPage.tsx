// Tenant Docker Apps page (M49, GH #170). Catalog grid of tenant-installable
// apps + an install modal (loopback-only, attached to a domain you own) + a
// table of your installs with start/stop/restart/delete. Degrades to a clear
// "not enabled on this server" notice when the host has no userns-remap (the
// backend 403s docker_tenant_not_enabled).
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { RowActions } from "../../../components/RowActions";
import { CatalogCard, CatalogGrid, CategoryFilter } from "../../../components/catalog";
import { Alert, Avatar, Button, AutoComplete, Col, Descriptions, Empty, Form, Input, Modal, Row, Space, Table, Tabs, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  AppstoreOutlined,
  DeleteOutlined,
  ExportOutlined,
  FileTextOutlined,
  KeyOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  SyncOutlined,
} from "@icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AxiosError } from "axios";
import { apiClient } from "../../../apiClient";
import { StatCard } from "../../../components/StatCard";
import { humanBytes } from "../../../utils/bytes";
import { useTabParam } from "../../../hooks/useTabParam";

import type { CatalogEntry, InstalledApp } from "../../admin/docker-apps/types";
import {
  catalogIconUrl,
  deleteApp,
  fetchEnv,
  fetchLogs,
  fetchUsage,
  installApp,
  lifecycleAction,
  listCatalog,
  listInstalled,
} from "./api";
import type { EnvVarView } from "./api";

const statusColor = (s: InstalledApp["status"]) =>
  s === "running"
    ? "green"
    : s === "failed"
      ? "red"
      : s === "installing" || s === "updating"
        ? "blue"
        : "default";

export const UserDockerAppsPage = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [installFor, setInstallFor] = useState<CatalogEntry | null>(null);
  const [logsFor, setLogsFor] = useState<InstalledApp | null>(null);
  const [credsFor, setCredsFor] = useState<InstalledApp | null>(null);

  const catalog = useQuery({ queryKey: ["user-docker-catalog"], queryFn: listCatalog });
  const [catalogTags, setCatalogTags] = useState<string[]>([]);
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
    queryKey: ["user-docker-installed"],
    queryFn: listInstalled,
    // Poll while anything is mid-install so the status tag flips live.
    refetchInterval: (q) =>
      (q.state.data ?? []).some((a) => a.status === "installing" || a.status === "updating")
        ? 8000
        : false,
  });

  const usage = useQuery({ queryKey: ["user-docker-usage"], queryFn: fetchUsage, retry: false });

  // The host-flag 403 surfaces on the installed-list query; treat it as the
  // "tenant docker disabled on this server" state.
  const disabled =
    (installed.error as AxiosError | undefined)?.response?.status === 403 ||
    (catalog.error as AxiosError | undefined)?.response?.status === 403;

  const lifecycle = useMutation({
    mutationFn: ({ id, action }: { id: string; action: "start" | "stop" | "restart" }) =>
      lifecycleAction(id, action),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["user-docker-installed"] }),
    onError: (e: AxiosError<{ detail?: string }>) =>
      feedback.message.error(`Action failed: ${e.response?.data?.detail}`),
  });
  const remove = useMutation({
    mutationFn: (id: string) => deleteApp(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["user-docker-installed"] }),
  });

  const rows = useMemo(() => installed.data ?? [], [installed.data]);
  const [installedSearch, setInstalledSearch] = useState("");
  const [tab, setTab] = useTabParam<string>("installed");
  const installedCount = rows.length;
  const runningCount = rows.filter((r) => r.status === "running").length;
  const stoppedCount = rows.filter((r) => r.status === "stopped").length;
  const updateCount = rows.filter(
    (r) => r.available_digest && r.available_digest !== (r.image_sha ?? ""),
  ).length;
  const catalogCount = (catalog.data ?? []).length;
  const pct = (n: number) => (installedCount > 0 ? Math.round((n / installedCount) * 100) : 0);

  if (disabled) {
    return (
      <div>
        <Typography.Title level={3} style={{ marginTop: 0 }}>
          <AppstoreOutlined /> Docker Apps
        </Typography.Title>
        <Alert
          type="info"
          showIcon
          message={t("userdockerappspage.docker_apps_are_not_enabled_on_this_server")}
          description={t("userdockerappspage.ask_your_administrator_to_enable_tenant_dock")}
        />
      </div>
    );
  }

  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        <AppstoreOutlined /> Docker Apps
      </Typography.Title>

      {usage.data?.over_quota && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("userdockerappspage.docker_apps_are_over_your_disk_quota")}
          description={t("userdockerappspage.your_installed_apps_exceed_your_package_disk")}
        />
      )}
      <Tabs
        activeKey={tab}
        onChange={setTab}
        items={[
          {
            key: "installed",
            label: "Installed",
            children: (
              <>
                <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
                  <Col xs={24} sm={12} lg={6}>
                    <StatCard
                      iconBg="rgba(207, 19, 34, 0.12)" iconColor="#cf1322" Icon={AppstoreOutlined}
                      label={t("userdockerappspage.installed_apps")} value={installedCount}
                      subtitle={<Typography.Text type="secondary">{catalogCount} in catalog</Typography.Text>}
                    />
                  </Col>
                  <Col xs={24} sm={12} lg={6}>
                    <StatCard
                      iconBg="rgba(114, 46, 209, 0.12)" iconColor="#722ed1" Icon={SyncOutlined}
                      label={t("userdockerappspage.updates_available")} value={updateCount}
                      subtitle={updateCount > 0
                        ? <Typography.Text type="warning">Needs attention</Typography.Text>
                        : <Typography.Text type="secondary">Up to date</Typography.Text>}
                    />
                  </Col>
                  <Col xs={24} sm={12} lg={6}>
                    <StatCard
                      iconBg="rgba(63, 134, 0, 0.12)" iconColor="#3f8600" Icon={PlayCircleOutlined}
                      label={t("userdockerappspage.running")} value={runningCount}
                      subtitle={<Typography.Text type="secondary">{pct(runningCount)}% of installed</Typography.Text>}
                    />
                  </Col>
                  <Col xs={24} sm={12} lg={6}>
                    <StatCard
                      iconBg="rgba(212, 107, 8, 0.12)" iconColor="#d46b08" Icon={PauseCircleOutlined}
                      label={t("userdockerappspage.stopped")} value={stoppedCount}
                      subtitle={<Typography.Text type="secondary">{pct(stoppedCount)}% of installed</Typography.Text>}
                    />
                  </Col>
                </Row>
                <Input.Search
                  allowClear
                  placeholder={t("userdockerappspage.search_by_name")}
                  value={installedSearch}
                  onChange={(e) => setInstalledSearch(e.target.value)}
                  style={{ marginBottom: 16, maxWidth: 360 }}
                />
                <Table<InstalledApp>
                  rowKey="id"
                  size="small"
                  loading={installed.isLoading}
                  dataSource={rows.filter((r) => {
                    const q = installedSearch.trim().toLowerCase();
                    if (!q) return true;
                    return (
                      r.name.toLowerCase().includes(q) ||
                      r.slug.toLowerCase().includes(q) ||
                      (r.domain ?? "").toLowerCase().includes(q)
                    );
                  })}
                  scroll={{ x: 1000 }}
                  pagination={{
                    defaultPageSize: 25,
                    showTotal: (total, range) => `Showing ${range[0]} to ${range[1]} of ${total} results`,
                  }}
                  locale={{
                    emptyText: (
                      <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("userdockerappspage.no_installed_apps_yet")}>
                        <Button type="primary" onClick={() => setTab("catalog")}>
                          Browse Catalog
                        </Button>
                      </Empty>
                    ),
                  }}
                  columns={[
                    {
                      title: "Name",
                      dataIndex: "name",
                      width: 240,
                      sorter: (a, b) => a.name.localeCompare(b.name),
                      defaultSortOrder: "ascend",
                      render: (n, r) => (
                        <Space size={12} align="start">
                          <Avatar shape="square" size={40} src={catalogIconUrl(r.slug)} style={{ background: "rgba(255,255,255,0.04)" }}>
                            {r.slug.slice(0, 2).toUpperCase()}
                          </Avatar>
                          <Space direction="vertical" size={0}>
                            <Typography.Text strong>{n}</Typography.Text>
                            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                              {r.slug} @ {r.catalog_version}
                            </Typography.Text>
                            {r.domain && (
                              <Typography.Link href={`https://${r.domain}/`} target="_blank" rel="noopener noreferrer" style={{ fontSize: 12 }}>
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
                      width: 160,
                      sorter: (a, b) => a.status.localeCompare(b.status),
                      render: (st: InstalledApp["status"], r) => (
                        <Space direction="vertical" size={4}>
                          <Space size={4}>
                            <Tag color={statusColor(st)}>{st}</Tag>
                            {(st === "installing" || st === "updating") && <SyncOutlined spin />}
                            {r.last_error && (
                              <Tooltip title={r.last_error}><Tag color="red">err</Tag></Tooltip>
                            )}
                          </Space>
                          {r.available_digest && r.available_digest !== (r.image_sha ?? "") && (
                            <Tag color="purple">update available</Tag>
                          )}
                        </Space>
                      ),
                    },
                    {
                      title: "Ports",
                      width: 300,
                      render: (_, r) => (
                        <Space direction="vertical" size={4} style={{ width: "100%" }}>
                          {(r.ports ?? []).map((p) => {
                            const proto = p.protocol === "tcp" ? "http" : p.protocol;
                            const href = `${proto === "http" ? "http" : "https"}://127.0.0.1:${p.host_port}`;
                            return (
                              <Space key={p.id} size={6}>
                                <Tag style={{ margin: 0, minWidth: 48, textAlign: "center" }}>{p.port_name}</Tag>
                                <Tag style={{ margin: 0 }}>
                                  <Space size={4}>
                                    <span>loopback:{p.host_port}/{p.protocol}</span>
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
                      width: 250,
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
                      width: 100,
                      sorter: (a, b) => (a.data_bytes ?? 0) - (b.data_bytes ?? 0),
                      render: (_, r) => (
                        <Typography.Text strong style={{ fontSize: 12 }}>
                          {r.data_bytes > 0 ? humanBytes(r.data_bytes) : "—"}
                        </Typography.Text>
                      ),
                    },
                    {
                      title: "Actions",
                      key: "actions",
                      width: 160,
                      align: "right" as const,
                      render: (_, r) => (
                        <RowActions
                          actions={[
                            { key: "start", label: "Start", icon: <PlayCircleOutlined />, hidden: r.status !== "stopped", onClick: () => lifecycle.mutate({ id: r.id, action: "start" }) },
                            { key: "stop", label: "Stop", icon: <PauseCircleOutlined />, hidden: r.status === "stopped", onClick: () => lifecycle.mutate({ id: r.id, action: "stop" }) },
                            { key: "restart", label: "Restart", icon: <ReloadOutlined />, onClick: () => lifecycle.mutate({ id: r.id, action: "restart" }) },
                            { key: "logs", label: "Logs", icon: <FileTextOutlined />, onClick: () => setLogsFor(r) },
                            { key: "creds", label: "Credentials", icon: <KeyOutlined />, onClick: () => setCredsFor(r) },
                            {
                              key: "delete",
                              label: "Delete",
                              icon: <DeleteOutlined />,
                              danger: true,
                              onClick: () => { void remove.mutateAsync(r.id); },
                              confirm: { title: `Delete "${r.name}"?`, description: "This removes the container and its data.", okText: "Delete" },
                            },
                          ]}
                        />
                      ),
                    },
                  ]}
                />
              </>
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
                      iconUrl={catalogIconUrl(e.slug)}
                      meta={<Tag style={{ marginInlineEnd: 0 }}>v{e.version}</Tag>}
                      description={e.description}
                      onInstall={() => setInstallFor(e)}
                    />
                  ))}
                  {catalog.data?.length === 0 && (
                    <Typography.Text type="secondary">No apps available to install.</Typography.Text>
                  )}
                </CatalogGrid>
              </>
            ),
          },
        ]}
      />

      <LogsModal app={logsFor} onClose={() => setLogsFor(null)} />
      <CredentialsModal app={credsFor} onClose={() => setCredsFor(null)} />

      <InstallModal
        entry={installFor}
        onClose={() => setInstallFor(null)}
        onInstalled={() => {
          setInstallFor(null);
          qc.invalidateQueries({ queryKey: ["user-docker-installed"] });
        }}
      />
    </div>
  );
};

const InstallModal = ({
  entry,
  onClose,
  onInstalled,
}: {
  entry: CatalogEntry | null;
  onClose: () => void;
  onInstalled: () => void;
}) => {
  const [form] = Form.useForm<{ name: string; domain: string }>();
  // #281: suggest the user's owned domains; AutoComplete (not a plain Select)
  // keeps a free hostname typeable since the backend accepts a new subdomain
  // under the caller's ownership too.
  const domainsQuery = useQuery({
    queryKey: ["user-domains-min"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: { id: string; name: string }[] }>(
        "/domains",
        { params: { page: 1, page_size: 100 } },
      );
      return data.data ?? [];
    },
  });
  const install = useMutation({
    mutationFn: (v: { name: string; domain: string }) =>
      installApp({ slug: entry!.slug, name: v.name, domain: v.domain }),
    onSuccess: () => {
      feedback.message.success("Install started");
      form.resetFields();
      onInstalled();
    },
    onError: (e: AxiosError<{ detail?: string; error?: string }>) =>
      feedback.message.error(`Install failed: ${e.response?.data?.detail || e.response?.data?.error}`),
  });

  return (
    <Modal
      open={!!entry}
      title={entry ? `Install ${entry.name}` : ""}
      onCancel={onClose}
      okText="Install"
      confirmLoading={install.isPending}
      onOk={() => form.validateFields().then((v) => install.mutate(v))}
    >
      <Form form={form} layout="vertical">
        <Form.Item
          name="name"
          label="Install name"
          rules={[
            { required: true, message: "Required" },
            { pattern: /^[a-z0-9-]{1,32}$/, message: "lowercase letters, digits, hyphens" },
          ]}
        >
          <Input placeholder="my-notes" />
        </Form.Item>
        <Form.Item
          name="domain"
          label="Domain"
          extra="A domain you own (or a new hostname). The app is served here over HTTPS."
          rules={[{ required: true, message: "Required" }]}
        >
          <AutoComplete
            placeholder="Select a domain you own, or type a new hostname"
            options={(domainsQuery.data ?? []).map((d) => ({ value: d.name }))}
            filterOption={(input, opt) =>
              (opt?.value ?? "").toLowerCase().includes(input.toLowerCase())
            }
          />
        </Form.Item>
      </Form>
    </Modal>
  );
};

const LogsModal = ({ app, onClose }: { app: InstalledApp | null; onClose: () => void }) => {
  const q = useQuery({
    queryKey: ["user-docker-logs", app?.id],
    queryFn: () => fetchLogs(app!.id),
    enabled: !!app,
  });
  return (
    <Modal
      open={!!app}
      title={app ? `Logs — ${app.name}` : ""}
      onCancel={onClose}
      onOk={onClose}
      width={760}
      footer={null}
    >
      {q.isLoading ? (
        "Loading…"
      ) : (
        <pre
          style={{
            maxHeight: 460,
            overflow: "auto",
            whiteSpace: "pre-wrap",
            wordBreak: "break-word",
            margin: 0,
            fontSize: 12,
          }}
        >
          {q.data?.logs || "(no output)"}
        </pre>
      )}
    </Modal>
  );
};

const CredentialsModal = ({ app, onClose }: { app: InstalledApp | null; onClose: () => void }) => {
  const q = useQuery({
    queryKey: ["user-docker-env", app?.id],
    queryFn: () => fetchEnv(app!.id),
    enabled: !!app,
  });
  return (
    <Modal
      open={!!app}
      title={app ? `Credentials — ${app.name}` : ""}
      onCancel={onClose}
      onOk={onClose}
      width={640}
      footer={null}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Generated secrets for this install (admin password, DB password, keys).
      </Typography.Paragraph>
      <Descriptions size="small" column={1} bordered>
        {(q.data ?? []).map((e: EnvVarView) => (
          <Descriptions.Item key={e.name} label={e.name}>
            <Typography.Text copyable={!!e.value} code>
              {e.value || "—"}
            </Typography.Text>
          </Descriptions.Item>
        ))}
      </Descriptions>
    </Modal>
  );
};

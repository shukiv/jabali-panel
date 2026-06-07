// AdminDockerAppsPage — landing page for the M48 marketplace.
// Two tabs: Catalog (browse + install) and Installed (lifecycle).
import { App, Avatar, Button, Card, Col, Dropdown, Empty, Modal, Row, Space, Table, Tabs, Tag, Tooltip, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
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
  MoreOutlined,
} from "@icons";

import { deleteApp, lifecycleAction, listCatalog, listInstalled, updateApp } from "./api";
import type { CatalogEntry, InstalledApp } from "./types";
import { InstallDrawer } from "./InstallDrawer";
import { LogsDrawer } from "./LogsDrawer";
import { ExecDrawer } from "./ExecDrawer";
import { BackupsDrawer } from "./BackupsDrawer";
import { EditDrawer } from "./EditDrawer";
import { MaintenanceTab } from "./MaintenanceTab";

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
  const { message } = App.useApp();
  const qc = useQueryClient();
  const [installEntry, setInstallEntry] = useState<CatalogEntry | null>(null);
  const [logsAppId, setLogsAppId] = useState<string | null>(null);
  const [execAppId, setExecAppId] = useState<string | null>(null);
  const [editApp, setEditApp] = useState<InstalledApp | null>(null);
  const [activeTab, setActiveTab] = useState<string>("installed");
  const [backupsAppId, setBackupsAppId] = useState<string | null>(null);

  const catalog = useQuery({
    queryKey: ["docker-apps-catalog"],
    queryFn: listCatalog,
  });
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
    onSuccess: (r) => {
      if (r.outcome === "rolled_back") {
        message.warning(r.detail ? `Rolled back: ${r.detail}` : "Update failed; rolled back to previous image");
      } else {
        message.success("Updated");
      }
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

  return (
    <div>
      <Typography.Title level={3} style={{ margin: 0, marginBottom: 16 }}>
        <ContainerOutlined /> Docker Apps
      </Typography.Title>

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
                  const rows = installed.data ?? [];
                  const installedCount = rows.length;
                  const runningCount = rows.filter(r => r.status === "running").length;
                  const stoppedCount = rows.filter(r => r.status === "stopped").length;
                  const updateCount = rows.filter(r => r.available_digest && r.available_digest !== r.image_sha).length;
                  const catalogCount = (catalog.data ?? []).length;
                  const pct = (n: number) => installedCount > 0 ? Math.round((n / installedCount) * 100) : 0;
                  const renderStat = (
                    iconBg: string,
                    iconColor: string,
                    Icon: React.ComponentType<{ style?: React.CSSProperties }>,
                    label: string,
                    value: number,
                    subtitle: React.ReactNode,
                  ) => (
                    <Card size="small">
                      <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
                        <div
                          style={{
                            width: 44,
                            height: 44,
                            borderRadius: 10,
                            background: iconBg,
                            color: iconColor,
                            display: "flex",
                            alignItems: "center",
                            justifyContent: "center",
                            flexShrink: 0,
                          }}
                        >
                          <Icon style={{ fontSize: 20 }} />
                        </div>
                        <div style={{ minWidth: 0, flex: 1 }}>
                          <Typography.Text type="secondary" style={{ fontSize: 12, display: "block" }}>
                            {label}
                          </Typography.Text>
                          <Typography.Text strong style={{ fontSize: 22, lineHeight: 1.2, display: "block" }}>
                            {value}
                          </Typography.Text>
                          <div style={{ fontSize: 11, marginTop: 2 }}>{subtitle}</div>
                        </div>
                      </div>
                    </Card>
                  );
                  return (
                    <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
                      <Col xs={24} sm={12} lg={6}>
                        {renderStat(
                          "rgba(207, 19, 34, 0.12)", "#cf1322", AppstoreOutlined,
                          "Installed Apps", installedCount,
                          <Typography.Text type="secondary">{catalogCount} in catalog</Typography.Text>,
                        )}
                      </Col>
                      <Col xs={24} sm={12} lg={6}>
                        {renderStat(
                          "rgba(114, 46, 209, 0.12)", "#722ed1", SyncOutlined,
                          "Updates Available", updateCount,
                          updateCount > 0
                            ? <Typography.Text type="warning">Needs attention</Typography.Text>
                            : <Typography.Text type="secondary">Up to date</Typography.Text>,
                        )}
                      </Col>
                      <Col xs={24} sm={12} lg={6}>
                        {renderStat(
                          "rgba(63, 134, 0, 0.12)", "#3f8600", PlayCircleOutlined,
                          "Running", runningCount,
                          <Typography.Text type="secondary">{pct(runningCount)}% of installed</Typography.Text>,
                        )}
                      </Col>
                      <Col xs={24} sm={12} lg={6}>
                        {renderStat(
                          "rgba(212, 107, 8, 0.12)", "#d46b08", PauseCircleOutlined,
                          "Stopped", stoppedCount,
                          <Typography.Text type="secondary">{pct(stoppedCount)}% of installed</Typography.Text>,
                        )}
                      </Col>
                    </Row>
                  );
                })()}
              <Table<InstalledApp>
                rowKey="id"
                size="small"
                loading={installed.isLoading}
                dataSource={installed.data ?? []}
                scroll={{ x: 1100 }}
                pagination={{
                  pageSize: 25,
                  showTotal: (total, range) => `Showing ${range[0]} to ${range[1]} of ${total} results`,
                }}
                locale={{
                  emptyText: (
                    <Empty
                      image={Empty.PRESENTED_IMAGE_SIMPLE}
                      description="No installed apps yet"
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
                    render: (n, r) => (
                      <Space size={12} align="start">
                        <Avatar
                          shape="square"
                          size={40}
                          src={`/api/v1/admin/docker-apps/catalog/${r.slug}/icon`}
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
                        </Space>
                      </Space>
                    ),
                  },
                  {
                    title: "Status",
                    dataIndex: "status",
                    width: 180,
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
                      return (
                        <Space size={4}>
                          {running ? (
                            <Tooltip title="Stop">
                              <Button
                                size="small"
                                icon={<PauseCircleOutlined />}
                                onClick={() => lifecycle.mutate({ id: r.id, action: "stop" })}
                              />
                            </Tooltip>
                          ) : (
                            <Tooltip title="Start">
                              <Button
                                size="small"
                                icon={<PlayCircleOutlined />}
                                onClick={() => lifecycle.mutate({ id: r.id, action: "start" })}
                              />
                            </Tooltip>
                          )}
                          <Tooltip title="Restart">
                            <Button
                              size="small"
                              icon={<ReloadOutlined />}
                              onClick={() => lifecycle.mutate({ id: r.id, action: "restart" })}
                            />
                          </Tooltip>
                          <Tooltip title={running ? "Stop the container before editing" : "Edit"}>
                            <Button
                              size="small"
                              icon={<EditOutlined />}
                              disabled={running}
                              onClick={() => setEditApp(r)}
                            />
                          </Tooltip>
                          <Dropdown
                            trigger={["click"]}
                            menu={{
                              items: [
                                {
                                  key: "update",
                                  icon: <SyncOutlined />,
                                  label: "Update",
                                  onClick: () => updateImage.mutate(r.id),
                                },
                                {
                                  key: "logs",
                                  icon: <FileTextOutlined />,
                                  label: "Logs",
                                  onClick: () => setLogsAppId(r.id),
                                },
                                {
                                  key: "exec",
                                  icon: <CodeOutlined />,
                                  label: "Exec",
                                  onClick: () => setExecAppId(r.id),
                                },
                                {
                                  key: "backups",
                                  icon: <SaveOutlined />,
                                  label: "Backups",
                                  onClick: () => setBackupsAppId(r.id),
                                },
                                { type: "divider" as const, key: "d1" },
                                {
                                  key: "delete",
                                  icon: <DeleteOutlined />,
                                  label: "Uninstall",
                                  danger: true,
                                  onClick: confirmDelete,
                                },
                              ],
                            }}
                          >
                            <Button size="small" icon={<MoreOutlined />} />
                          </Dropdown>
                        </Space>
                      );
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
              <div
                style={{
                  columnGap: 16,
                  // CSS shorthand: as many columns of >=320px as the
                  // viewport fits. Works at mobile (1 col), tablet (2),
                  // desktop (3+) without media queries.
                  columns: "320px",
                }}
                className="docker-apps-catalog-masonry"
              >
                {(catalog.data ?? []).map((e) => (
                  <div
                    key={e.slug}
                    style={{ breakInside: "avoid", marginBottom: 16, display: "inline-block", width: "100%" }}
                  >
                    <Card
                      hoverable
                      onClick={() => setInstallEntry(e)}
                      styles={{ body: { padding: 16 } }}
                      actions={[<Button type="link" key="install">Install</Button>]}
                    >
                      <Card.Meta
                        avatar={
                          <Avatar
                            shape="square"
                            size={40}
                            src={`/api/v1/admin/docker-apps/catalog/${e.slug}/icon`}
                            style={{ backgroundColor: "transparent", color: "#1f1f1f" }}
                          >
                            {e.name[0]}
                          </Avatar>
                        }
                        title={
                          <Space size={6} wrap>
                            <span>{e.name}</span>
                            <Tag style={{ marginInlineEnd: 0 }}>{e.version}</Tag>
                          </Space>
                        }
                        description={
                          <div style={{ whiteSpace: "pre-line" }}>
                            {e.description}
                          </div>
                        }
                      />
                    </Card>
                  </div>
                ))}
              </div>
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
    </div>
  );
};

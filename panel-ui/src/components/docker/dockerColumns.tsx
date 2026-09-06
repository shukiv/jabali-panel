// dockerColumns — the shared Installed-app table columns for the admin and
// tenant Docker inventories (JAB-335). Column set and order are identical for
// both screens; the only audience-driven differences are folded in through the
// column context:
//   - portPresentation: "loopback-only" (tenant) forces every port to render as
//     127.0.0.1/loopback REGARDLESS of the row's bind_interface — that is the
//     load-bearing isolation invariant (ADR-0117), not a cosmetic default;
//     "bind-aware" (admin) honours the row's bind_interface.
//   - privilegedActions: an OPTIONAL builder the admin supplies (edit / update /
//     exec / backups) and the tenant OMITS. The privileged verbs are ABSENT
//     from the tenant action list by construction — there is no per-verb boolean
//     to get wrong, so the tenant UI cannot render or dispatch them.
import type { ReactNode } from "react";
import { Alert, Avatar, Space, Tag, Tooltip, Typography, type TableColumnsType } from "antd";
import {
  DeleteOutlined,
  ExportOutlined,
  FileTextOutlined,
  KeyOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  SyncOutlined,
} from "@icons";

import { humanBytes } from "../../utils/bytes";
import { RowActions, type RowAction } from "../RowActions";
import type { InstalledApp } from "../../shells/admin/docker-apps/types";
import { TRANSITIONAL_STATUSES } from "./dockerStatus";

// Admin's superset palette — the tenant screen previously lacked stopped=orange
// and rolling_back=purple; consolidating takes the richer set for both.
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

export type PortPresentation = "loopback-only" | "bind-aware";

export interface DockerRemovePolicy {
  label: string; // "Uninstall" (admin) / "Delete" (tenant)
  confirm: (app: InstalledApp) => { title: string; description: string; okText: string };
}

export interface DockerColumnContext {
  catalogIconUrl: (slug: string) => string;
  portPresentation: PortPresentation;
  onLifecycle: (app: InstalledApp, action: "start" | "stop" | "restart") => void;
  onLogs: (app: InstalledApp) => void;
  onCredentials: (app: InstalledApp) => void;
  onDelete: (app: InstalledApp) => void;
  remove: DockerRemovePolicy;
  // Admin supplies privileged verbs; tenant omits the field entirely.
  privilegedActions?: (app: InstalledApp) => RowAction[];
}

const nameColumn = (ctx: DockerColumnContext) => ({
  title: "Name",
  dataIndex: "name" as const,
  width: 260,
  sorter: (a: InstalledApp, b: InstalledApp) => a.name.localeCompare(b.name),
  defaultSortOrder: "ascend" as const,
  render: (n: string, r: InstalledApp): ReactNode => (
    <Space size={12} align="start">
      <Avatar shape="square" size={40} src={ctx.catalogIconUrl(r.slug)} style={{ background: "rgba(255,255,255,0.04)" }}>
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
              <Typography.Text style={{ fontSize: 11, whiteSpace: "normal" }}>{r.post_install_note}</Typography.Text>
            }
          />
        )}
      </Space>
    </Space>
  ),
});

const statusColumn = () => ({
  title: "Status",
  dataIndex: "status" as const,
  width: 180,
  sorter: (a: InstalledApp, b: InstalledApp) => a.status.localeCompare(b.status),
  render: (s: string, r: InstalledApp): ReactNode => (
    <Space direction="vertical" size={4}>
      <Space size={4}>
        <Tag color={STATUS_COLOR[s] || "default"}>{s}</Tag>
        {TRANSITIONAL_STATUSES.has(s) && <SyncOutlined spin />}
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
});

const portsColumn = (ctx: DockerColumnContext) => ({
  title: "Ports",
  dataIndex: "ports" as const,
  width: 320,
  render: (_: unknown, r: InstalledApp): ReactNode => (
    <Space direction="vertical" size={4} style={{ width: "100%" }}>
      {(r.ports ?? []).map((p) => {
        // loopback-only forces isolation regardless of the row's bind_interface.
        const loopback =
          ctx.portPresentation === "loopback-only" ||
          p.bind_interface === "loopback" ||
          p.bind_interface === "127.0.0.1";
        const bindLabel = loopback ? "loopback" : "public";
        const linkHost = loopback ? "127.0.0.1" : window.location.hostname;
        const proto = p.protocol === "tcp" ? "http" : p.protocol;
        const href = `${proto === "http" ? "http" : "https"}://${linkHost}:${p.host_port}`;
        return (
          <Space key={p.id} size={6} style={{ width: "100%" }}>
            <Tag style={{ margin: 0, minWidth: 48, textAlign: "center" }}>{p.port_name}</Tag>
            <Tag style={{ margin: 0 }}>
              <Space size={4}>
                <span>
                  {bindLabel}:{p.host_port}/{p.protocol}
                </span>
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
});

const limitsColumn = () => ({
  title: "Limits",
  width: 280,
  render: (_: unknown, r: InstalledApp): ReactNode => (
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
});

const diskColumn = () => ({
  title: "Disk",
  dataIndex: "data_bytes" as const,
  width: 110,
  sorter: (a: InstalledApp, b: InstalledApp) => (a.data_bytes ?? 0) - (b.data_bytes ?? 0),
  render: (_: unknown, r: InstalledApp): ReactNode => (
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
});

const actionsColumn = (ctx: DockerColumnContext) => ({
  title: "Actions",
  key: "actions",
  width: 180,
  align: "right" as const,
  render: (_: unknown, r: InstalledApp): ReactNode => {
    const running = r.status === "running";
    const actions: RowAction[] = [
      running
        ? { key: "stop", label: "Stop", icon: <PauseCircleOutlined />, onClick: () => ctx.onLifecycle(r, "stop") }
        : { key: "start", label: "Start", icon: <PlayCircleOutlined />, onClick: () => ctx.onLifecycle(r, "start") },
      { key: "restart", label: "Restart", icon: <ReloadOutlined />, onClick: () => ctx.onLifecycle(r, "restart") },
      // Privileged verbs (edit / update / exec / backups) exist only when the
      // admin audience supplies them; the tenant audience omits the field, so
      // they are absent from the row — not rendered, not dispatchable.
      ...(ctx.privilegedActions ? ctx.privilegedActions(r) : []),
      { key: "logs", label: "Logs", icon: <FileTextOutlined />, onClick: () => ctx.onLogs(r) },
      { key: "creds", label: "Credentials", icon: <KeyOutlined />, onClick: () => ctx.onCredentials(r) },
      {
        key: "delete",
        label: ctx.remove.label,
        icon: <DeleteOutlined />,
        danger: true,
        onClick: () => ctx.onDelete(r),
        confirm: ctx.remove.confirm(r),
      },
    ];
    return <RowActions actions={actions} />;
  },
});

export const dockerInstalledColumns = (ctx: DockerColumnContext): TableColumnsType<InstalledApp> => [
  nameColumn(ctx),
  statusColumn(),
  portsColumn(ctx),
  limitsColumn(),
  diskColumn(),
  actionsColumn(ctx),
];

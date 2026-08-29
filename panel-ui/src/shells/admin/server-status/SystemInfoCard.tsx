// SystemInfoCard — categorized "category | property | value" table for
// the Server Status page top section. Replaces the old HostHeaderCard
// banner with structured rows the operator can scan at a glance.
//
// Categories: Server (host identity), Hardware (CPU/RAM), Network
// (interfaces). Source data lives in envelope.host + envelope.network.
import { Card, Space, Table, Tag, Typography } from "antd";

import { CheckCircleOutlined, ExclamationCircleOutlined, InfoCircleOutlined } from "@ant-design/icons";

import { RebootServerButton } from "./RebootServerButton";

import type {
  HostSlice,
  NetworkSlice,
  SoftwareSlice,
} from "../../../hooks/useServerStatus";

type CategoryColor = "blue" | "purple" | "geekblue";

interface Row {
  key: string;
  category: string;
  categoryColor: CategoryColor;
  property: string;
  value: React.ReactNode;
}

interface Props {
  host: HostSlice | null;
  network: NetworkSlice | null;
  software: SoftwareSlice | null;
  asOf: string;
}

export function SystemInfoCard({ host, network, software, asOf }: Props) {
  const rows: Row[] = [];

  rows.push({
    key: "hostname", category: "Server", categoryColor: "blue",
    property: "Hostname",
    value: <code>{host?.hostname ?? "—"}</code>,
  });
  rows.push({
    key: "uptime", category: "Server", categoryColor: "blue",
    property: "Uptime",
    value: <code>{humanizeUptime(host?.uptime_seconds ?? 0)}</code>,
  });
  rows.push({
    key: "os", category: "Server", categoryColor: "blue",
    property: "OS",
    value: <code>{host?.os ?? "—"}</code>,
  });
  rows.push({
    key: "kernel", category: "Server", categoryColor: "blue",
    property: "Kernel",
    value: <code>{host?.kernel ?? "—"}</code>,
  });
  rows.push({
    key: "tz", category: "Server", categoryColor: "blue",
    property: "Timezone",
    value: <code>{host?.timezone ?? "—"}</code>,
  });
  rows.push({
    key: "ntp", category: "Server", categoryColor: "blue",
    property: "Time sync",
    value: host?.ntp_synced ? (
      <Tag color="green" icon={<CheckCircleOutlined />}>NTP synced</Tag>
    ) : (
      <Tag color="orange" icon={<ExclamationCircleOutlined />}>NTP unsynced</Tag>
    ),
  });

  // First non-loopback IPv4 surfaces here; the full network table is a
  // separate card below.
  const primaryIPv4 = (network?.interfaces ?? [])
    .flatMap((i) => i.ipv4 ?? [])
    .find((ip) => ip && !ip.startsWith("127."));
  if (primaryIPv4) {
    rows.push({
      key: "ip", category: "Server", categoryColor: "blue",
      property: "IP address",
      value: <code>{primaryIPv4}</code>,
    });
  }

  rows.push({
    key: "cpu_model", category: "Hardware", categoryColor: "purple",
    property: "Processor",
    value: <code>{host?.cpu_model ?? "—"}</code>,
  });
  rows.push({
    key: "cpu_count", category: "Hardware", categoryColor: "purple",
    property: "CPU cores",
    value: <code>{host?.cpu_count ?? "—"}</code>,
  });
  if (host?.mem_total_kb) {
    const usedPct = (host.mem_used_kb / host.mem_total_kb) * 100;
    rows.push({
      key: "memory", category: "Hardware", categoryColor: "purple",
      property: "Memory",
      value: <code>{`${humanKB(host.mem_used_kb)} / ${humanKB(host.mem_total_kb)} (${usedPct.toFixed(1)}%)`}</code>,
    });
  }
  if (host?.swap_total_kb) {
    rows.push({
      key: "swap", category: "Hardware", categoryColor: "purple",
      property: "Swap",
      value: <code>{`${humanKB(host.swap_used_kb)} / ${humanKB(host.swap_total_kb)}`}</code>,
    });
  }

  // Software stack — versions of installed binaries the panel manages.
  // Backed by system.software (5min cache) so this is cheap to surface
  // here even though the parent envelope refetches every 5s.
  for (const item of software?.items ?? []) {
    rows.push({
      key: `sw-${item.name}`,
      category: "Software",
      categoryColor: "geekblue",
      property: item.name,
      value: item.version
        ? <code>{item.version}</code>
        : <Typography.Text type="secondary">not installed</Typography.Text>,
    });
  }

  return (
    <Card
      title={<><InfoCircleOutlined /> System Information</>}
      size="small"
      extra={
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          Updated {humanizeAgo(asOf)}
        </Typography.Text>
      }
    >
      <Table<Row>
        rowKey="key"
        size="small"
        dataSource={rows}
        pagination={false}
        scroll={{ x: "max-content" }}
        showHeader={false}
        // No fixed column widths — let value (hostnames, IPv6 addrs,
        // 50+ char processor model strings) stretch to its natural
        // length. scroll x=max-content keeps the table within the card
        // and adds horizontal scroll if the row exceeds viewport.
        // Category Tag stays tight via its own width; property uses
        // ellipsis only when actually overflowing, not preemptively.
        columns={[
          {
            dataIndex: "category",
            render: (_: unknown, r: Row) => (
              <Tag color={r.categoryColor}>{r.category}</Tag>
            ),
          },
          { dataIndex: "property" },
          {
            dataIndex: "value",
            // value is React.ReactNode (<code>hostname</code>, <Tag>...</Tag>,
            // etc.) — render as-is + wrap in a wordBreak span so long
            // IPv6/processor strings can break inside the cell when the
            // table scrolls horizontally. Stringifying broke the e2e
            // hostname assertion (getByText 'mx.jabali-panel.local').
            render: (v: React.ReactNode) => (
              <span style={{ wordBreak: "break-all" }}>{v}</span>
            ),
          },
        ]}
      />
      {/* GH #1330: server power action lives with the host identity it acts on. */}
      <Space style={{ width: "100%", justifyContent: "flex-end", marginTop: 12 }}>
        <RebootServerButton>Reboot server</RebootServerButton>
      </Space>
    </Card>
  );
}

function humanizeUptime(secs: number): string {
  if (!secs) return "—";
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = Math.floor(secs % 60);
  if (d > 0) return `${d}d ${h}h ${m}m ${s}s`;
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

function humanizeAgo(iso: string): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "—";
  const ago = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (ago < 5) return "just now";
  if (ago < 60) return `${ago}s ago`;
  return `${Math.round(ago / 60)}m ago`;
}

function humanKB(kb: number): string {
  if (!kb) return "0";
  const units = ["KB", "MB", "GB", "TB"];
  let v = kb;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
}

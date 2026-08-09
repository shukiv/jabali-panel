import { Card, Space, Table, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { GlobalOutlined } from "@ant-design/icons";

import type { NetworkInterface } from "../../../hooks/useServerStatus";

interface Props {
  interfaces: NetworkInterface[];
}

export function NetworkTable({ interfaces }: Props) {
  return (
    <Card title={<><GlobalOutlined /> Network</>} size="small">
      <Table<NetworkInterface>
        rowKey="iface"
        size="small"
        dataSource={interfaces}
        pagination={false}
        scroll={{ x: "max-content" }}
        rowClassName={(r) => (r.state === "DOWN" ? "row-net-down" : "")}
        columns={[
          { title: "Iface", dataIndex: "iface" },
          {
            title: "State",
            dataIndex: "state",
            render: (s: string) => (
              <Tag color={s === "UP" ? "green" : s === "DOWN" ? "red" : "default"}>
                {s}
              </Tag>
            ),
          },
          {
            title: "IPv4",
            render: (_, r) => <IPChips ips={r.ipv4} />,
          },
          {
            title: "IPv6",
            render: (_, r) => <IPChips ips={r.ipv6} />,
          },
          {
            title: "RX",
            render: (_, r) =>
              r.warming_up ? "—" : `${humanBps(r.rx_bps)} (${r.rx_pps}/s)`,
          },
          {
            title: "TX",
            render: (_, r) =>
              r.warming_up ? "—" : `${humanBps(r.tx_bps)} (${r.tx_pps}/s)`,
          },
          {
            title: "Errors",
            render: (_, r) => `${r.rx_errors} / ${r.tx_errors}`,
          },
        ]}
      />
    </Card>
  );
}

function IPChips({ ips }: { ips: string[] }) {
  if (!ips || ips.length === 0) return <Typography.Text type="secondary">—</Typography.Text>;
  return (
    <Space size={4} wrap>
      {ips.map((ip) => (
        <Tag
          key={ip}
          style={{ cursor: "pointer" }}
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(ip);
              feedback.message.success("Copied " + ip);
            } catch {
              // ignore
            }
          }}
        >
          {ip}
        </Tag>
      ))}
    </Space>
  );
}

function humanBps(bps: number): string {
  if (!bps) return "0 B/s";
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  let v = bps;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
}

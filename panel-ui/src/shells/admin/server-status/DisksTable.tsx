import { Card, Progress, Table, Tag } from "antd";
import { HddOutlined } from "@ant-design/icons";

import { humanBytes } from "../../../utils/bytes";
import type { Partition } from "../../../hooks/useServerStatus";

interface Props {
  partitions: Partition[];
}

export function DisksTable({ partitions }: Props) {
  return (
    <Card title={<><HddOutlined /> Disks</>} size="small">
      <Table<Partition>
        rowKey="mount_point"
        size="small"
        dataSource={partitions}
        pagination={false}
        scroll={{ x: "max-content" }}
        rowClassName={(r) => {
          const pct = pctOf(r);
          if (pct >= 95) return "row-disk-critical";
          if (pct >= 80) return "row-disk-warning";
          return "";
        }}
        columns={[
          { title: "Mount", dataIndex: "mount_point" },
          {
            title: "Used",
            render: (_, r) => (
              <div style={{ minWidth: 200 }}>
                <Progress
                  percent={pctOf(r)}
                  size="small"
                  strokeColor={diskColor(pctOf(r))}
                />
                <span style={{ fontSize: 11 }}>
                  {humanBytes(r.used_bytes)} / {humanBytes(r.total_bytes)}
                </span>
              </div>
            ),
          },
          {
            title: "Free",
            render: (_, r) => humanBytes(r.free_bytes),
          },
          {
            title: "Status",
            render: (_, r) => {
              const p = pctOf(r);
              if (p >= 95) return <Tag color="red">critical</Tag>;
              if (p >= 80) return <Tag color="orange">warning</Tag>;
              return <Tag color="green">healthy</Tag>;
            },
          },
        ]}
      />
    </Card>
  );
}

// pctOf is df's Use% (GH #2029): used as a share of the space non-root can
// use, rounded up. Blocks ext4 reserves for root count as neither used nor
// free, so used + free is less than the size. The panel's disk alerts use the
// same number (internal/fsusage).
function pctOf(p: Partition): number {
  const usable = p.used_bytes + p.free_bytes;
  if (!usable) return 0;
  return Math.ceil((p.used_bytes * 100) / usable);
}

function diskColor(pct: number): string {
  if (pct >= 95) return "#cf1322";
  if (pct >= 80) return "#fa8c16";
  return "#52c41a";
}


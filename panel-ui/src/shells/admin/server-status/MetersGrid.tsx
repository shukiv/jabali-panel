// MetersGrid — individual CPU/Memory/Swap/Load meter cards.
//
// Each meter is a standalone <Card>; ServerStatusPage drops them into
// the Masonry layout as individual children so they flow with every
// other card on the page rather than monopolising a full row.
import { Card, Progress, Space, Tooltip, Typography } from "antd";
import { ApiOutlined, DatabaseOutlined } from "@ant-design/icons";

import type { CPUSlice, HostSlice } from "../../../hooks/useServerStatus";
import { usageColor } from "./usage";

interface MeterProps {
  host: HostSlice | null;
  cpu: CPUSlice | null;
}

// CPUMeterCard renders CPU usage + 1/5/15m load averages in a single
// card. Load averages live in the card's `extra` slot (top-right of
// the title bar) so the body stays a single clean Progress block.
export function CPUMeterCard({ host, cpu }: MeterProps) {
  const cpuPct = cpu?.usage_percent ?? 0;
  const cpuWarming = cpu?.warming_up ?? true;
  const cores = host?.cpu_count ?? 1;
  const load1 = host?.load_avg?.[0] ?? 0;
  const load5 = host?.load_avg?.[1] ?? 0;
  const load15 = host?.load_avg?.[2] ?? 0;
  return (
    <Card
      title={<><ApiOutlined /> CPU</>}
      size="small"
      extra={
        <Tooltip title="Load average · 1m / 5m / 15m">
          <Space size={4} style={{ fontSize: 12 }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>load</Typography.Text>
            <LoadInline value={load1} cores={cores} />
            <Typography.Text type="secondary">/</Typography.Text>
            <LoadInline value={load5} cores={cores} />
            <Typography.Text type="secondary">/</Typography.Text>
            <LoadInline value={load15} cores={cores} />
          </Space>
        </Tooltip>
      }
    >
      <Progress
        percent={Math.round(cpuPct)}
        strokeColor={usageColor(cpuPct)}
        status={cpuWarming ? "active" : undefined}
      />
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        iowait {cpu?.iowait_percent?.toFixed(1) ?? "0.0"}% · {cores} core
        {cores === 1 ? "" : "s"}
        {cpuWarming ? " · warming up" : ""}
      </Typography.Text>
    </Card>
  );
}

// LoadInline renders one load value with traffic-light coloring. Used
// in the CPU card's title-bar extra slot.
function LoadInline({ value, cores }: { value: number; cores: number }) {
  let color: string | undefined;
  if (value > cores * 2) color = "#cf1322";
  else if (value > cores) color = "#fa8c16";
  else color = "#52c41a";
  return <span style={{ color, fontWeight: 600 }}>{value.toFixed(2)}</span>;
}

// MemoryMeterCard renders Memory + Swap stacked in a single card.
// Same physical resource family (RAM + paged-out RAM); operators
// already read them together when diagnosing pressure. Combining
// halves the masonry slot count without losing any data.
export function MemoryMeterCard({ host }: MeterProps) {
  const memUsedPct =
    host && host.mem_total_kb > 0
      ? (host.mem_used_kb / host.mem_total_kb) * 100
      : 0;
  const hasSwap = !!host && host.swap_total_kb > 0;
  const swapUsedPct = hasSwap ? (host!.swap_used_kb / host!.swap_total_kb) * 100 : 0;
  return (
    <Card title={<><DatabaseOutlined /> Memory</>} size="small">
      <div>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          RAM
        </Typography.Text>
        <Progress
          percent={Math.round(memUsedPct)}
          strokeColor={usageColor(memUsedPct)}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {humanKB(host?.mem_used_kb ?? 0)} / {humanKB(host?.mem_total_kb ?? 0)}
        </Typography.Text>
      </div>
      <div style={{ marginTop: 12 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          Swap
        </Typography.Text>
        {hasSwap ? (
          <>
            <Progress
              percent={Math.round(swapUsedPct)}
              strokeColor={usageColor(swapUsedPct)}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {humanKB(host!.swap_used_kb)} / {humanKB(host!.swap_total_kb)}
            </Typography.Text>
          </>
        ) : (
          <div>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              No swap configured
            </Typography.Text>
          </div>
        )}
      </div>
    </Card>
  );
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

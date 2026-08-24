// ServerStatusPage — admin landing for live host health (M31, ADR-0065).
//
// One TanStack Query hits /admin/server-status every 5s while the tab
// is foreground. All sub-sections share the same envelope so the page
// renders cohesively even when one slice is null (timeout / agent
// hiccup) — empty cells show "—" rather than ghost numbers.
//
// Layout: AntD Masonry. Note that AntD's Masonry does NOT render
// arbitrary children — it only renders nodes supplied via `items[]`
// (each item.children is what gets placed). Putting JSX between
// <Masonry> tags would silently render nothing, which is what made
// this page show a blank canvas during the first cut.
import { Masonry, Typography } from "antd";
import { ChartBarOutlined } from "@icons";

import { useServerStatus } from "../../../hooks/useServerStatus";
import { AlertsBanner } from "./AlertsBanner";
import { DisksTable } from "./DisksTable";
import { DRStatusCard } from "./DRStatusCard";
import { CPUMeterCard, MemoryMeterCard } from "./MetersGrid";
import { NetworkTable } from "./NetworkTable";
import { ProcessesCard } from "./ProcessesCard";
import { QueuesCard } from "./QueuesCard";
import { ServicesSummaryCard } from "./ServicesSummaryCard";
import { SpeedTestCard } from "./SpeedTestCard";
import { SystemInfoCard } from "./SystemInfoCard";
import { UserSlicesCard } from "./UserSlicesCard";

export const ServerStatusPage = () => {
  const q = useServerStatus();
  const env = q.data;

  // Order matters: Masonry fills column-first, so the first N items
  // become the top row across the columns. Operator wants CPU/Memory/
  // Swap visible above the fold — put the meters first.
  const items = [
    { key: "cpu", data: null, children: <CPUMeterCard host={env?.host ?? null} cpu={env?.cpu ?? null} /> },
    { key: "memory", data: null, children: <MemoryMeterCard host={env?.host ?? null} cpu={env?.cpu ?? null} /> },
    { key: "disks", data: null, children: <DisksTable partitions={env?.host?.partitions ?? []} /> },
    { key: "network", data: null, children: <NetworkTable interfaces={env?.network?.interfaces ?? []} /> },
    { key: "services", data: null, children: <ServicesSummaryCard services={env?.services?.services ?? []} /> },
    { key: "dr", data: null, children: <DRStatusCard /> },
    {
      key: "sysinfo",
      data: null,
      children: (
        <SystemInfoCard
          host={env?.host ?? null}
          network={env?.network ?? null}
          software={env?.software ?? null}
          asOf={env?.as_of ?? ""}
        />
      ),
    },
    { key: "user_slices", data: null, children: <UserSlicesCard data={env?.user_slices ?? null} /> },
    { key: "processes", data: null, children: <ProcessesCard processes={env?.processes ?? null} /> },
    { key: "queues", data: null, children: <QueuesCard queues={env?.queues ?? null} /> },
    { key: "speedtest", data: null, children: <SpeedTestCard /> },
  ];

  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <ChartBarOutlined /> Server Status
      </Typography.Title>

      <AlertsBanner alerts={env?.alerts ?? []} />

      <Masonry columns={{ xs: 1, sm: 1, md: 2, lg: 2 }} gutter={16} items={items} />
    </div>
  );
};

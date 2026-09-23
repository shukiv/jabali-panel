// StaleSlicesNotice — surfaces JAB-373 AC#5 stale-serve metadata on the Server
// Status page. When the aggregator cannot refresh a slice before its deadline it
// serves the slice's last-good value and flags it `stale` in the envelope `meta`,
// with the `observed_at` of the cached body. Without this, a stale card is
// indistinguishable from a live one — an operator who "just fixed a disk" could
// stare at last-known numbers and think the fix did nothing.
//
// This renders a compact banner naming the stale slices, with each one's observed
// time on hover, and renders nothing when every served slice is fresh (the common
// case). It is deliberately display-only and never derives an alert — alert
// synthesis reads only fresh bodies (server_status.go), and this component mirrors
// that separation on the client.
import { Alert, Space, Tag, Tooltip } from "antd";

import type { SliceMeta } from "../../../hooks/useServerStatus";

// Human labels for the aggregator's slice keys (server_status.go call names).
// Unknown keys fall back to the raw key so a newly added slice still surfaces.
const SLICE_LABELS: Record<string, string> = {
  host: "System info",
  cpu: "CPU",
  network: "Network",
  processes: "Processes",
  services: "Services",
  user_slices: "User slices",
  software: "Software",
  nginx: "Nginx",
  apparmor: "AppArmor",
  queues: "Queues",
};

function labelFor(key: string): string {
  return SLICE_LABELS[key] ?? key;
}

export interface StaleSlicesNoticeProps {
  meta?: Record<string, SliceMeta>;
}

export const StaleSlicesNotice = ({ meta }: StaleSlicesNoticeProps) => {
  const staleKeys = meta ? Object.keys(meta).filter((k) => meta[k]?.stale).sort() : [];
  if (staleKeys.length === 0) {
    return null;
  }

  return (
    <Alert
      type="warning"
      showIcon
      style={{ marginBottom: 16 }}
      title="Showing last-known values"
      description={
        <Space size={[8, 8]} wrap>
          {staleKeys.map((k) => (
            <Tooltip key={k} title={`Last observed ${meta?.[k]?.observed_at ?? "unknown"}`}>
              <Tag color="orange" data-testid={`stale-slice-${k}`}>
                {labelFor(k)}
              </Tag>
            </Tooltip>
          ))}
        </Space>
      }
    />
  );
};

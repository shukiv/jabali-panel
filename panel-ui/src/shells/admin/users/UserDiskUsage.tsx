// Plain "used / limit" disk-usage cell for the admin Users list.
//
// Disk usage doesn't change second-to-second — quotacheck reports are
// usually minutes-stale on the kernel side anyway. Cache aggressively
// in the client so navigating away and back doesn't refetch, and the
// cell is instant on revisit. Server-side has its own 60s TTL on the
// agent call (panel-api/internal/api/user_limits.go), so even a hard
// page reload within the window hits a warm cache server-side.
//
// No 5s polling. If an operator wants a fresh number, they hit Cmd+R.
import { useQuery } from "@tanstack/react-query";
import { Progress, Typography } from "antd";

import { apiClient } from "../../../apiClient";

type UsageResponse = {
  effective?: { DiskQuotaMB?: number };
  current?: { disk?: { used_kb?: number; limit_kb?: number } };
};

const STALE_MS = 5 * 60 * 1000;
const GC_MS = 30 * 60 * 1000;

function formatBytes(bytes: number): string {
  if (bytes <= 0) return "0";
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(0)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

export function UserDiskUsage({ userId }: { userId: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ["user-usage", userId],
    queryFn: async () => {
      const resp = await apiClient.get<UsageResponse>(`/users/${userId}/usage`);
      return resp.data;
    },
    staleTime: STALE_MS,
    gcTime: GC_MS,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    refetchOnMount: false,
  });

  if (isLoading && !data) {
    return <Typography.Text type="secondary">…</Typography.Text>;
  }

  const usedKB = data?.current?.disk?.used_kb ?? 0;
  // 0 = "no quota plumbing" (e.g. quotacheck couldn't run on busy /).
  // Fall through to effective.DiskQuotaMB so the package limit renders.
  const reportedLimitKB = data?.current?.disk?.limit_kb ?? 0;
  const limitKB =
    reportedLimitKB > 0
      ? reportedLimitKB
      : data?.effective?.DiskQuotaMB
        ? data.effective.DiskQuotaMB * 1024
        : 0;

  if (limitKB > 0) {
    const pct = Math.min(100, Math.round((usedKB / limitKB) * 100));
    return (
      <div style={{ minWidth: 130, maxWidth: 180 }}>
        <Progress
          percent={pct}
          size="small"
          status={pct >= 95 ? "exception" : "normal"}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {`${formatBytes(usedKB * 1024)} / ${formatBytes(limitKB * 1024)}`}
        </Typography.Text>
      </div>
    );
  }
  if (usedKB > 0) {
    return <span>{formatBytes(usedKB * 1024)}</span>;
  }
  return <Typography.Text type="secondary">—</Typography.Text>;
}

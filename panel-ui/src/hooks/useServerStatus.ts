// useServerStatus — TanStack Query for /admin/server-status.
//
// 5s polling while the tab is foreground; pauses when hidden so an idle
// tab is zero load on the agent. The aggregator response has typed top-
// level fields and three sub-objects we decode lazily because each
// belongs to a different page section.
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export interface ServerStatusEnvelope {
  as_of: string;
  host: HostSlice | null;
  cpu: CPUSlice | null;
  network: NetworkSlice | null;
  processes: ProcessesSlice | null;
  services: ServicesSlice | null;
  user_slices: UserSlicesSlice | null;
  software: SoftwareSlice | null;
  queues: QueuesSlice | null;
  errors?: Record<string, string>;
  alerts: Alert[];
}

export interface QueuesSlice {
  notifications_queue: number;
  notifications_dlq: number;
  notifications_pending: number;
}

export interface SoftwareSlice {
  items: SoftwareItem[];
}

export interface SoftwareItem {
  name: string;
  version: string;
}

export interface UserSlicesSlice {
  slices: UserSliceMetric[];
  warming_up: boolean;
  as_of: string;
}

export interface UserSliceMetric {
  username: string;
  cpu_percent: number;
  memory_bytes: number;
  memory_max_bytes: number;
  tasks: number;
}

export interface HostSlice {
  hostname: string;
  os: string;
  kernel: string;
  cpu_model: string;
  timezone: string;
  uptime_seconds: number;
  load_avg: [number, number, number];
  cpu_count: number;
  mem_total_kb: number;
  mem_available_kb: number;
  mem_used_kb: number;
  swap_total_kb: number;
  swap_used_kb: number;
  partitions: Partition[];
  ntp_synced: boolean;
}

export interface Partition {
  mount_point: string;
  total_bytes: number;
  used_bytes: number;
  free_bytes: number;
}

export interface CPUSlice {
  usage_percent: number;
  iowait_percent: number;
  per_core: number[];
  warming_up: boolean;
  as_of: string;
}

export interface NetworkSlice {
  interfaces: NetworkInterface[];
  as_of: string;
}

export interface NetworkInterface {
  iface: string;
  state: string;
  mac?: string;
  mtu?: number;
  ipv4: string[];
  ipv6: string[];
  rx_bps: number;
  tx_bps: number;
  rx_pps: number;
  tx_pps: number;
  rx_errors: number;
  tx_errors: number;
  warming_up: boolean;
}

export interface ProcessesSlice {
  total: number;
  running: number;
  sleeping: number;
  zombie: number;
  stopped: number;
  other: number;
  top_by_rss: ProcessTop[];
  top_by_cpu: ProcessTop[];
}

export interface ProcessTop {
  pid: number;
  comm: string;
  user: string;
  rss_kb: number;
  state: string;
  cpu_percent: number;
}

export interface ServicesSlice {
  services: ServiceDetail[];
}

export interface ServiceDetail {
  unit: string;
  active: string;
  sub: string;
  load_state: string;
  unit_file_state: string;
  memory_bytes: number;
  tasks: number;
  uptime_seconds: number;
  active_entered_at?: string;
}

export interface Alert {
  level: "warning" | "critical";
  kind: string;
  detail: string;
}

export function useServerStatus(opts?: { enabled?: boolean; refetchInterval?: number }) {
  return useQuery<ServerStatusEnvelope>({
    queryKey: ["admin", "server-status"],
    queryFn: async () => {
      const r = await apiClient.get<ServerStatusEnvelope>("/admin/server-status");
      return r.data;
    },
    // Shares the cache key with the Server Status page, so a caller that
    // only needs one field (e.g. host.cpu_count in the docker-app
    // drawers) reuses an already-fetched payload and can gate its own
    // polling with `enabled` instead of fetching the heavy aggregate
    // on every render.
    enabled: opts?.enabled ?? true,
    // refetchInterval lets a low-priority subscriber (the header health
    // badge, mounted on every admin page) ask for a slower cadence. React
    // Query dedupes across observers of one key and honours the SHORTEST
    // requested interval, so the badge costs nothing extra while a page
    // with a faster cadence is open, and drops to its own interval once
    // that page unmounts.
    refetchInterval: opts?.refetchInterval ?? 5000,
    refetchIntervalInBackground: false,
  });
}

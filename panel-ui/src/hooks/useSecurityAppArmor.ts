// useSecurityAppArmor — TanStack Query hooks for the M40 AppArmor
// admin endpoints. Wire contract per panel-api/internal/api/security_apparmor.go.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export interface AppArmorProfile {
  name: string;
  mode: "enforce" | "complain" | "missing" | "kernel-gated";
}

export interface AppArmorDenial {
  timestamp: string;
  profile: string;
  operation: string;
  path?: string;
  requested_mask?: string;
  denied_mask?: string;
  comm?: string;
  exe?: string;
  pid?: string;
  fsuid?: string;
}

export interface AppArmorStatus {
  enabled: boolean;
  reason?: string;
  profiles: AppArmorProfile[];
  denials: AppArmorDenial[];
  violations: AppArmorDenial[];
}

// apiClient baseURL is already "/api/v1" — paths must be relative
// to that, NOT include the prefix again (would produce
// /api/v1/api/v1/... → 404).
const BASE = "/admin/security/apparmor";

export function useAppArmorStatus() {
  return useQuery({
    queryKey: ["security", "apparmor", "status"],
    queryFn: async () => {
      const { data } = await apiClient.get<AppArmorStatus>(`${BASE}/status`);
      return data;
    },
    refetchInterval: 60_000,
  });
}

// GH #2001: what an allowed PHP command-execution function can start on this
// server, from the mode of the profile PHP-FPM runs under. "enforce": only the
// shell and cat; "complain" (it only logs) or "none" (not loaded, kernel-gated,
// or AppArmor off): any program. undefined while unknown.
export type FpmExecConfinement = "enforce" | "complain" | "none";

export function fpmExecConfinement(status: AppArmorStatus | undefined): FpmExecConfinement | undefined {
  if (!status || !Array.isArray(status.profiles)) return undefined;
  if (!status.enabled) return "none";
  const mode = status.profiles.find((p) => p.name === "jabali-fpm-app")?.mode;
  if (mode === "enforce") return "enforce";
  if (mode === "complain") return "complain";
  return "none";
}

// One status read for the package editor: same cache as useAppArmorStatus,
// without its 60s polling.
export function useFpmExecConfinement(): FpmExecConfinement | undefined {
  const q = useQuery({
    queryKey: ["security", "apparmor", "status"],
    queryFn: async () => {
      const { data } = await apiClient.get<AppArmorStatus>(`${BASE}/status`);
      return data;
    },
    staleTime: 60_000,
  });
  return fpmExecConfinement(q.data);
}

export function useSetAppArmorMode() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (args: { profile: string; mode: "enforce" | "complain" }) => {
      const { data } = await apiClient.post(
        `${BASE}/profiles/${args.profile}/mode`,
        { mode: args.mode },
      );
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security", "apparmor"] }),
  });
}

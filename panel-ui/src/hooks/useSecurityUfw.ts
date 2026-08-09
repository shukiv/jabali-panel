// useSecurityUfw — TanStack Query hooks for the M26 UFW admin
// endpoints. Wire contract per panel-api/internal/api/security_ufw.go.
//
// Enable / disable POST bodies MUST include {"confirm":"YES"} —
// returning 400 confirmation_required otherwise. The UI surfaces a
// feedback.modal.confirm with a typed YES gate before invoking these hooks.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export type UfwAction = "allow" | "deny" | "reject";
export type UfwProto = "tcp" | "udp";

export type UfwRule = {
  num: number;
  action: string;
  from: string;
  to: string;
  proto?: string;
  port?: string;
};

export type UfwStatus = {
  active: boolean;
  default_in: string;
  default_out: string;
  rules: UfwRule[];
};

const BASE = "/admin/security/ufw";

export function useUfwStatus() {
  return useQuery({
    queryKey: ["security", "ufw", "status"],
    queryFn: async () => {
      const { data } = await apiClient.get<UfwStatus>(`${BASE}/status`);
      return data;
    },
    refetchInterval: 30_000,
  });
}

export function useAddUfwRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      action: UfwAction;
      port: string;
      proto?: UfwProto;
      from?: string;
    }) => {
      const { data } = await apiClient.post<{ added: boolean; rule_num: number }>(
        `${BASE}/rules`,
        input,
      );
      return data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["security", "ufw", "status"] });
    },
  });
}

export function useDeleteUfwRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (num: number) => {
      await apiClient.delete(`${BASE}/rules/${num}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["security", "ufw", "status"] });
    },
  });
}

export function useUfwToggle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ enable }: { enable: boolean }) => {
      const path = enable ? `${BASE}/enable` : `${BASE}/disable`;
      const { data } = await apiClient.post<{ active: boolean }>(path, {
        confirm: "YES",
      });
      return data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["security", "ufw", "status"] });
    },
  });
}

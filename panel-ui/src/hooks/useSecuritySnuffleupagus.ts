// useSecuritySnuffleupagus — TanStack Query hooks for the M41
// Snuffleupagus admin endpoints. Mirrors the M40 AppArmor hook shape
// (and the same lesson: BASE is relative to apiClient.baseURL = "/api/v1",
// no double-prefix).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export type SnuffleupagusMode = "off" | "simulation" | "enforce";

export interface SnuffleupagusPhpVersion {
  minor: string;
  extension_so: string;
  loaded: boolean;
}

export interface SnuffleupagusStatus {
  enabled: boolean;
  mode: SnuffleupagusMode;
  active_rules_sha256?: string;
  last_applied_at?: string;
  rules_count?: number;
  php_versions_loaded: SnuffleupagusPhpVersion[];
}

export interface SnuffleupagusRule {
  name: string;
  source_file: string;
  enabled: boolean;
  reason?: string;
}

export interface SnuffleupagusIncident {
  id: number;
  ts: string;
  rule_name: string;
  action: "log" | "block" | "simulated_block";
  source_ip?: string;
  request_uri?: string;
  domain?: string;
  php_version?: string;
}

const BASE = "/admin/security/snuffleupagus";

export function useSnuffleupagusStatus() {
  return useQuery({
    queryKey: ["security", "snuffleupagus", "status"],
    queryFn: async () => {
      const { data } = await apiClient.get<SnuffleupagusStatus>(`${BASE}/status`);
      return data;
    },
    refetchInterval: 30_000,
  });
}

export function useSetSnuffleupagusMode() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (mode: SnuffleupagusMode) => {
      const { data } = await apiClient.post(`${BASE}/mode`, { mode });
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security", "snuffleupagus"] }),
  });
}

export function useSnuffleupagusRules() {
  return useQuery({
    queryKey: ["security", "snuffleupagus", "rules"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ rules: SnuffleupagusRule[] }>(`${BASE}/rules`);
      return data.rules;
    },
  });
}

export function useToggleSnuffleupagusRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (args: { name: string; enabled: boolean; reason?: string }) => {
      const { data } = await apiClient.post(
        `${BASE}/rules/${encodeURIComponent(args.name)}/toggle`,
        { enabled: args.enabled, reason: args.reason },
      );
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security", "snuffleupagus"] }),
  });
}

// GH #2262: Clear history deletes every recorded incident.
export function useClearSnuffleupagusIncidents() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.delete<{ deleted: number }>(`${BASE}/incidents`);
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security", "snuffleupagus"] }),
  });
}

export interface IncidentsQuery {
  since?: string;
  limit?: number;
  rule?: string;
  domain?: string;
  page?: number;
}

export function useSnuffleupagusIncidents(q: IncidentsQuery = {}) {
  const params = new URLSearchParams();
  if (q.since) params.set("since", q.since);
  if (q.limit) params.set("limit", String(q.limit));
  if (q.rule) params.set("rule", q.rule);
  if (q.domain) params.set("domain", q.domain);
  if (q.page) params.set("page", String(q.page));
  return useQuery({
    queryKey: ["security", "snuffleupagus", "incidents", params.toString()],
    queryFn: async () => {
      const { data } = await apiClient.get<{
        data: SnuffleupagusIncident[];
        total: number;
        page: number;
        page_size: number;
      }>(`${BASE}/incidents?${params.toString()}`);
      return data;
    },
    refetchInterval: 30_000,
  });
}

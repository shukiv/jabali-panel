// useSecurityAide — TanStack Query hooks for the M42 AIDE FIM
// admin endpoints. Wire contract per panel-api/internal/api/security_aide.go.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export interface AideSampleRow {
  path: string;
  change_type: "added" | "changed" | "removed";
  category: string;
  meta?: { attr: string; old: string; new: string }[];
}

export interface AideStatus {
  enabled: boolean;
  reason?: string;
  db_age_seconds: number;
  last_check_ts?: string;
  summary: {
    added: number;
    changed: number;
    removed: number;
  };
  sample: AideSampleRow[];
  categories?: Record<string, number>;
  // GH #2079: the rebuild and the check run in the background.
  rebuild?: AideRebuild;
  check?: { running: boolean; failed?: boolean };
  // Set instead of last_check_ts when the last aide run was the rebuild.
  baseline_rebuilt_at?: string;
}

// AideRebuild is the baseline rebuild started from the panel: running now, or
// how the last one ended (ok, or error says why).
export interface AideRebuild {
  running: boolean;
  ok?: boolean;
  error?: string;
  started_at?: string;
  finished_at?: string;
}

// apiClient baseURL is already "/api/v1" — paths must be relative
// to that, NOT include the prefix again (would produce
// /api/v1/api/v1/... → 404).
const BASE = "/admin/security/aide";

export function useAideStatus() {
  return useQuery({
    queryKey: ["security", "aide", "status"],
    queryFn: async () => {
      const { data } = await apiClient.get<AideStatus>(`${BASE}/status`);
      return data;
    },
    // Poll faster while a rebuild or a check runs, so the page shows the end.
    refetchInterval: (query) =>
      query.state.data?.rebuild?.running || query.state.data?.check?.running ? 5_000 : 60_000,
  });
}

// GH #2079: the reason a start failed (a rebuild or a check already running,
// systemd-run's error) is in the error body's detail.
export function aideErrorMessage(prefix: string, err: unknown): string {
  const e = err as { response?: { data?: { detail?: string; error?: string } }; message?: string };
  const reason = (e?.response?.data?.detail || e?.response?.data?.error || e?.message || "").trim();
  if (!reason) return prefix;
  return `${prefix}: ${reason.length > 600 ? `${reason.slice(0, 600)}…` : reason}`;
}

export function useRunAideCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<{ started?: boolean }>(`${BASE}/check`);
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security", "aide"] }),
  });
}

// useRunAideRebuild starts a rebuild of the AIDE baseline in the background
// (GH #2079); the status reports it. dryRun returns the plan only. Gitea #561.
export function useRunAideRebuild() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (dryRun: boolean) => {
      const { data } = await apiClient.post<{ started?: boolean; dry_run?: boolean; plan?: string }>(
        `${BASE}/rebuild`,
        // GH #683: real rebuild requires the confirmation token; the Rebuild
        // button is already behind a confirm dialog.
        dryRun ? { dry_run: true } : { dry_run: false, confirm: "REBUILD" },
      );
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security", "aide"] }),
  });
}

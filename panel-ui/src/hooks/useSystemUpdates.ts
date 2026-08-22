// useSystemUpdates — TanStack Query wrappers for /api/v1/admin/updates/*.
//
// `check` queries are mutations not queries: they trigger work (apt-get
// update, git fetch) so re-running them is a deliberate operator action,
// not something we want to auto-fire. Status queries refresh on a 2-second
// interval as long as the corresponding unit is "active" or "activating".
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export interface CommitSummary {
  sha: string;
  subject: string;
  date: string;
}

export interface JabaliCheckResult {
  current_sha: string;
  remote_sha: string;
  behind_count: number;
  branch: string;
  recent_commits?: CommitSummary[];
  // Commits in HEAD..origin/main — what "Update now" will apply (GH #300).
  pending_commits?: CommitSummary[];
}

export interface AptPackage {
  name: string;
  current: string;
  new: string;
  source: string;
  security?: boolean;
}

// AptCheckError (JAB-10) — structured diagnostic when the package check itself
// failed. reason is a stable key: apt_locked | repo_unreachable | permission |
// command_failed. When present, total/packages are meaningless.
export interface AptCheckError {
  reason: string;
  command: string;
  exit_code: number;
  stderr: string;
  hint: string;
}

export interface AptCheckResult {
  packages: AptPackage[];
  total: number;
  security_total?: number;
  installed_total?: number;
  error?: AptCheckError | null;
}

export interface RunResult {
  unit: string;
  started_at: string;
}

export interface UnitStatus {
  unit: string;
  status: string;
  exit_code?: number;
  log_tail: string;
  fetched_at: string;
}

export function useJabaliCheck() {
  return useMutation<JabaliCheckResult>({
    mutationFn: async () => {
      const r = await apiClient.get<JabaliCheckResult>("/admin/updates/jabali/check");
      return r.data;
    },
  });
}

export function useJabaliRun() {
  const qc = useQueryClient();
  return useMutation<RunResult>({
    mutationFn: async () => {
      const r = await apiClient.post<RunResult>("/admin/updates/jabali/run");
      return r.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jabali-status"] });
      qc.invalidateQueries({ queryKey: ["admin-active-tasks"] }); // pop the tasks icon now

    },
  });
}

export function useJabaliStatus(since: string | null) {
  return useQuery<UnitStatus>({
    queryKey: ["jabali-status", since],
    queryFn: async () => {
      const r = await apiClient.get<UnitStatus>(
        `/admin/updates/jabali/status${since ? `?since=${encodeURIComponent(since)}` : ""}`,
      );
      return r.data;
    },
    // Always enabled (not gated on `since`): a run started elsewhere — CLI,
    // tasks indicator, or a reload mid-update — still surfaces its log. With
    // no `since` the agent returns the last 15m of the unit.
    enabled: true,
    refetchInterval: (q) => {
      const s = q.state.data?.status;
      // Poll while the unit is alive — 2s gives a snappy log tail. Stop
      // polling once it terminates so we don't hit the agent forever.
      return s === "active" || s === "activating" ? 2000 : false;
    },
  });
}

export function useJabaliStop() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const r = await apiClient.delete("/admin/updates/jabali");
      return r.data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["jabali-status"] }),
  });
}

export function useAptCheck() {
  return useMutation<AptCheckResult>({
    mutationFn: async () => {
      const r = await apiClient.get<AptCheckResult>("/admin/updates/apt/check");
      return r.data;
    },
  });
}

export function useAptRun() {
  const qc = useQueryClient();
  return useMutation<RunResult>({
    mutationFn: async () => {
      const r = await apiClient.post<RunResult>("/admin/updates/apt/run");
      return r.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["apt-status"] });
      qc.invalidateQueries({ queryKey: ["admin-active-tasks"] }); // pop the tasks icon now
    },
  });
}

export function useAptStatus(since: string | null) {
  return useQuery<UnitStatus>({
    queryKey: ["apt-status", since],
    queryFn: async () => {
      const r = await apiClient.get<UnitStatus>(
        `/admin/updates/apt/status${since ? `?since=${encodeURIComponent(since)}` : ""}`,
      );
      return r.data;
    },
    // Always enabled (not gated on `since`): a run started elsewhere — CLI,
    // tasks indicator, or a reload mid-update — still surfaces its log. With
    // no `since` the agent returns the last 15m of the unit.
    enabled: true,
    refetchInterval: (q) => {
      const s = q.state.data?.status;
      return s === "active" || s === "activating" ? 2000 : false;
    },
  });
}

export function useAptStop() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const r = await apiClient.delete("/admin/updates/apt");
      return r.data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["apt-status"] }),
  });
}

// --- M53 Updates Center: state, history, auto-update, changelog ------------

export interface UpdateState {
  jabali_behind: number;
  jabali_current_sha?: string;
  jabali_checked_at?: string;
  apt_total: number;
  apt_security: number;
  apt_checked_at?: string;
  // JAB-353 OS-patch status.
  apt_last_applied_at?: string;
  apt_reboot_required?: boolean;
}

export interface UpdateHistoryRow {
  id: string;
  kind: string;
  action: string;
  status: string;
  security_count: number;
  summary: string;
  unit?: string;
  started_at: string;
  finished_at?: string;
}

export interface AutoupdateConfig {
  apt_enabled: boolean;
  // JAB-353: must be sent true to disable OS security auto-updates. The API
  // rejects a disable without it; the UI collects it via a confirm modal.
  apt_optout_acknowledged?: boolean;
  apt_time: string;
  jabali_enabled: boolean;
  jabali_time: string;
}

// useUpdateState reads the persisted last-check snapshot for instant page
// load. Refetched after a check completes (the check hooks invalidate it).
export function useUpdateState() {
  return useQuery<UpdateState>({
    queryKey: ["update-state"],
    queryFn: async () => {
      const r = await apiClient.get<UpdateState>("/admin/updates/state");
      return r.data;
    },
  });
}

export function useUpdateHistory(limit = 20) {
  return useQuery<{ items: UpdateHistoryRow[]; total: number }>({
    queryKey: ["update-history", limit],
    queryFn: async () => {
      const r = await apiClient.get<{ items: UpdateHistoryRow[]; total: number }>(
        `/admin/updates/history?limit=${limit}`,
      );
      return r.data;
    },
  });
}

export function useAutoupdateConfig() {
  return useQuery<AutoupdateConfig>({
    queryKey: ["update-autoupdate"],
    queryFn: async () => {
      const r = await apiClient.get<AutoupdateConfig>("/admin/updates/autoupdate");
      return r.data;
    },
  });
}

export function useUpdateAutoupdate() {
  const qc = useQueryClient();
  return useMutation<AutoupdateConfig, Error, AutoupdateConfig>({
    mutationFn: async (cfg) => {
      const r = await apiClient.put<AutoupdateConfig>("/admin/updates/autoupdate", cfg);
      return r.data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["update-autoupdate"] }),
  });
}



// --- repair (Repair Center on the Updates page) ----------------------------

export interface RepairDiagnoseResult {
  output: string;
  exit_code: number;
}

// useRepairDiagnose runs `jabali repair --diagnose` (read-only) and returns
// the detector text. Synchronous on the server — no polling.
export function useRepairDiagnose() {
  return useMutation<RepairDiagnoseResult>({
    mutationFn: async () => {
      const r = await apiClient.post<RepairDiagnoseResult>(
        "/admin/updates/repair/diagnose",
      );
      return r.data;
    },
  });
}

// useRepairRun fires `jabali repair --auto` as a transient unit; poll
// useRepairStatus(started_at) for progress + output.
export function useRepairRun() {
  return useMutation<RunResult>({
    mutationFn: async () => {
      const r = await apiClient.post<RunResult>("/admin/updates/repair/run");
      return r.data;
    },
  });
}

export function useRepairStatus(since: string | null) {
  return useQuery<UnitStatus>({
    queryKey: ["repair-status", since],
    queryFn: async () => {
      const r = await apiClient.get<UnitStatus>(
        `/admin/updates/repair/status${since ? `?since=${encodeURIComponent(since)}` : ""}`,
      );
      return r.data;
    },
    enabled: since !== null,
    refetchInterval: (q) => {
      const s = q.state.data?.status;
      return s === "active" || s === "activating" ? 2000 : false;
    },
  });
}

export function useRepairStop() {
  return useMutation({
    mutationFn: async () => {
      const r = await apiClient.delete("/admin/updates/repair");
      return r.data;
    },
  });
}

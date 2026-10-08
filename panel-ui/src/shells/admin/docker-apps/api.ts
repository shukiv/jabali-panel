// Wire calls for the M48 admin docker-apps surface.
import { apiClient } from "../../../apiClient";
import type {
  CatalogEntry,
  InstallRequest,
  InstalledApp,
} from "./types";

const BASE = "/admin/docker-apps";

export async function listCatalog(): Promise<CatalogEntry[]> {
  const { data } = await apiClient.get<{ items: CatalogEntry[] }>(`${BASE}/catalog`);
  return data.items || [];
}

export async function listInstalled(): Promise<InstalledApp[]> {
  const { data } = await apiClient.get<{ items: InstalledApp[] }>(BASE);
  return data.items || [];
}

export async function installApp(req: InstallRequest): Promise<InstalledApp> {
  // Override the 15s default: install pulls a fresh image + starts the
  // compose project + (optionally) waits for healthcheck. 10 min ceiling
  // matches the backend agent's docker_app.install timeout.
  const { data } = await apiClient.post<InstalledApp>(BASE, req, { timeout: 10 * 60 * 1000 });
  return data;
}

export async function deleteApp(id: string, keepVolumes = false): Promise<void> {
  const qs = keepVolumes ? "?keep_volumes=1" : "";
  await apiClient.delete(`${BASE}/${id}${qs}`);
}

export async function lifecycleAction(
  id: string,
  action: "start" | "stop" | "restart" | "rebuild",
): Promise<void> {
  await apiClient.post(`${BASE}/${id}/${action}`);
}

export interface UpdateStarted {
  status: string;
  id: string;
  // GH #1956: set when the update doesn't take the app to the catalog's
  // newest version (it stays on its release track, or steps to the next
  // major first), saying why.
  notice?: string;
}

export async function updateApp(id: string): Promise<UpdateStarted> {
  // The server now starts the update asynchronously and returns 202 with
  // { status: "updating" } — the pull + recreate runs in the background and
  // the row's status (polled every 8s) flips to running/failed. (Was
  // synchronous; long image pulls blew past nginx's proxy timeout -> 502.)
  const { data } = await apiClient.post<UpdateStarted>(`${BASE}/${id}/update`, undefined);
  return data;
}

export interface LogsResponse {
  slug: string;
  logs: string;
}

export async function fetchLogs(id: string, lines = 200): Promise<LogsResponse> {
  const { data } = await apiClient.get<LogsResponse>(`${BASE}/${id}/logs?lines=${lines}`);
  return data;
}

export interface ExecResponse {
  slug: string;
  exit_code: number;
  stdout: string;
  stderr: string;
}

export async function execCmd(id: string, command: string, service?: string): Promise<ExecResponse> {
  const { data } = await apiClient.post<ExecResponse>(`${BASE}/${id}/exec`, {
    command,
    ...(service ? { service } : {}),
  });
  return data;
}

export interface BackupRow {
  id: string;
  time: string;
  hostname?: string;
  tags?: string[];
}
export interface ListBackupsResponse {
  slug: string;
  backups: BackupRow[];
}
export async function listBackups(id: string): Promise<ListBackupsResponse> {
  const { data } = await apiClient.get<ListBackupsResponse>(`${BASE}/${id}/backups`);
  return data;
}
export async function createBackup(id: string): Promise<{ snapshot_id: string; size_bytes?: number }> {
  const { data } = await apiClient.post<{ snapshot_id: string; size_bytes?: number }>(`${BASE}/${id}/backup`);
  return data;
}
export async function restoreBackup(id: string, snapshotId: string): Promise<void> {
  await apiClient.post(`${BASE}/${id}/backups/${snapshotId}/restore`);
}


export interface PatchRequest {
  update_mode?: "manual" | "auto";
  cpu_limit?: string;
  memory_limit?: string;
  pids_limit?: number;
  domain?: string;
  ports?: Array<{
    name: string;
    enabled?: boolean;
    bind_interface?: string;
    host_port?: number;
    reverse_proxy?: boolean;
  }>;
}

export async function patchApp(id: string, body: PatchRequest): Promise<void> {
  await apiClient.patch(`${BASE}/${id}`, body);
}

// ---- environment (view / edit / regenerate) -------------------------------

export interface EnvVar {
  name: string;
  value: string;
  secret: boolean;
  generated: boolean;
}

export async function getEnv(id: string): Promise<EnvVar[]> {
  const { data } = await apiClient.get<{ env: EnvVar[] }>(`${BASE}/${id}/env`);
  return data.env || [];
}

export async function putEnv(id: string, env: Record<string, string>): Promise<void> {
  // Re-renders the compose + recreates the container — allow a few minutes.
  await apiClient.put(`${BASE}/${id}/env`, { env }, { timeout: 5 * 60 * 1000 });
}

export async function regenerateEnv(
  id: string,
  key: string,
): Promise<{ key: string; value: string }> {
  const { data } = await apiClient.post<{ key: string; value: string }>(
    `${BASE}/${id}/env/regenerate`,
    { key },
    { timeout: 5 * 60 * 1000 },
  );
  return data;
}

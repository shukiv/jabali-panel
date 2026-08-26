// apiClient.ts — the one axios instance the whole SPA uses.
//
// M20: authentication is entirely cookie-based via Kratos. The browser
// automatically attaches the `ory_kratos_session` cookie on same-origin
// requests (we serve /.ory/* and /api/v1/* from the same vhost for
// exactly this reason — see install/nginx/.ory-location.conf and the
// panel's main vhost block). There is no bearer token in JavaScript;
// the refresh dance is gone. When Kratos rejects the session, /api/v1/*
// returns 401 and the caller lets Refine's authProvider.onError route
// to /login.

import axios, { type AxiosError } from "axios";

const API_BASE = "/api/v1";

export const apiClient = axios.create({
  baseURL: API_BASE,
  withCredentials: true, // send the Kratos session cookie
  // 15s hard ceiling — without a timeout, any network hang (proxy, dropped
  // connection, Firefox's Opaque-Response-Blocking cache caught mid-flight)
  // freezes <Authenticated>'s check() indefinitely and the SPA renders
  // blank. Anything legitimate on this API completes in <1s.
  timeout: 15000,
});

// ADR-0128 — inject the admin act-as grant id on every request when one is
// active (per-tab sessionStorage). Read inline (not via impersonation.ts) to
// avoid an import cycle. panel-api ignores it unless the real Kratos cookie is
// an admin who owns the grant.
apiClient.interceptors.request.use((config) => {
  try {
    const raw = sessionStorage.getItem("jabali_act_as");
    if (raw) {
      const grant = JSON.parse(raw) as { id?: string };
      if (grant?.id) {
        config.headers = config.headers ?? {};
        config.headers["X-Jabali-Act-As"] = grant.id;
      }
    }
  } catch {
    // sessionStorage unavailable — proceed without the header
  }
  return config;
});

apiClient.interceptors.response.use(
  (resp) => resp,
  (err: AxiosError) => {
    // JAB-177: rewrite a demo write-guard rejection to a friendly sentence at
    // this one choke point (see friendlyDemoError) so every rendering path
    // shows readable copy instead of the raw "demo_mode" code.
    friendlyDemoError(
      err.response?.status,
      err.response?.data as { error?: string; detail?: string } | undefined,
    );

    // A dead/expired/foreign act-as grant: drop the grant and bounce back to
    // the admin view rather than silently continuing as the admin.
    const data = err.response?.data as { error?: string } | undefined;
    if (err.response?.status === 403 && data?.error === "impersonation_invalid") {
      try {
        sessionStorage.removeItem("jabali_act_as");
      } catch {
        // ignore
      }
      if (typeof window !== "undefined") {
        window.location.assign("/jabali-admin");
      }
    }

    // JAB-380 recent-auth step-up: a root-privileged surface (admin File
    // Manager / Root Terminal) rejected the request because the Kratos session
    // wasn't authenticated recently enough. Send the user through a Kratos
    // refresh login, then Kratos returns them to the current page to retry.
    // `stepup_unavailable` (API-token / impersonated callers) is NOT redirected
    // — there is no interactive session to refresh; it surfaces as a message.
    if (err.response?.status === 403 && data?.error === "stepup_required") {
      stepUpRedirect();
    }

    return Promise.reject(normalizeError(err));
  },
);

/**
 * JAB-380: redirect the browser through a Kratos *refresh* login for a
 * recent-auth (step-up) challenge on the root File Manager / Root Terminal.
 * Kratos re-authenticates the existing session (bumping authenticated_at) and
 * the panel's Login page returns the user to where they were, to repeat the
 * action against a now-fresh session.
 *
 * The return path is carried in sessionStorage `post_login_return_to`, NOT the
 * Kratos `return_to` query — that query is dropped on the
 * /self-service/login/browser → /login hop (see the M20.1 note in Login.tsx).
 * This mirrors MyProfile's refresh-flow escalation exactly. The stashed value
 * is a path (starts with "/") so it satisfies Login.tsx's same-origin guard;
 * the query is still appended belt-and-braces. Exported for tests; guarded so
 * it is inert during SSR/tests without a window.
 */
export function stepUpRedirect(): void {
  if (typeof window === "undefined" || !window.location) return;
  const returnTo = window.location.pathname + window.location.search;
  try {
    sessionStorage.setItem("post_login_return_to", returnTo);
  } catch {
    // sessionStorage unavailable — the refresh still works; the user just
    // lands on their role home instead of back on this exact page.
  }
  const url =
    "/.ory/self-service/login/browser?refresh=true&return_to=" +
    encodeURIComponent(returnTo);
  window.location.assign(url);
}

/**
 * JAB-177: the demo write-guard rejects every write with
 * 403 {"error":"demo_mode"}. Call sites vary — some show err.message
 * (normalized below), some read response.data.error/.detail raw — so a handful
 * surfaced the literal "demo_mode" code, which reads like a bug on the public
 * demo. Rewrite the body IN PLACE to a friendly sentence (preferring the
 * backend-supplied banner detail) so every rendering path shows readable copy.
 * Returns true when it applied. Exported for tests.
 */
export function friendlyDemoError(
  status: number | undefined,
  data: { error?: string; detail?: string } | undefined,
): boolean {
  if (status !== 403 || !data || data.error !== "demo_mode") return false;
  const friendly =
    data.detail && data.detail !== "demo_mode"
      ? data.detail
      : "This is a read-only demo — changes are disabled.";
  data.error = friendly;
  data.detail = friendly;
  return true;
}

/**
 * Normalize axios errors by extracting the backend's structured error response.
 * Converts {"error":"domain_already_exists","detail":"..."} into a readable message.
 * Refine's notification provider will call err.message, so we set that field.
 */
function normalizeError(err: AxiosError): AxiosError {
  const status = err.response?.status;
  const data = err.response?.data as { error?: string; detail?: string } | undefined;
  const code = data?.error;
  const detail = data?.detail;

  // Prefer detail field if present, else humanize the error code, else fallback to original message.
  const message =
    detail ??
    (code ? humanizeErrorCode(code) : undefined) ??
    err.message ??
    `Request failed with status ${status ?? "unknown"}`;

  // Copy the error to preserve status, response, etc, but override the message.
  const wrapped = new Error(message) as AxiosError;
  wrapped.name = err.name;
  wrapped.config = err.config;
  wrapped.code = err.code;
  wrapped.request = err.request;
  wrapped.response = err.response;
  wrapped.isAxiosError = err.isAxiosError;
  wrapped.status = err.status;
  wrapped.toJSON = err.toJSON.bind(err);

  return wrapped;
}

/**
 * Human-friendly messages for common backend error codes.
 * Falls back to the code with underscores replaced by spaces if not found.
 */
function humanizeErrorCode(code: string): string {
  const messages: Record<string, string> = {
    domain_already_exists: "That domain is already taken",
    domain_quota_exceeded: "Your plan doesn't allow more domains",
    admin_cannot_host: "Admins can't host domains — create a regular user first",
    os_user_exists: "A Linux user with that name already exists",
    admin_has_no_os_account: "This user is an admin and has no OS account",
    cannot_delete_self: "You can't delete your own account",
    cannot_delete_last_admin: "Can't delete the last admin",
    unauthenticated: "Please log in again",
    missing_session: "Please log in again",
    invalid_session: "Session expired — please log in again",
    identity_service_unavailable: "Identity service temporarily unavailable — try again shortly",
    validation_failed: "Some fields are invalid",
    stepup_required: "Re-authenticate to continue — redirecting you to sign in again…",
    stepup_unavailable:
      "This action needs an interactive browser session with a recent login (API tokens and act-as sessions can't perform it)",
    internal: "Something went wrong on the server",
    agent_error:
      "The server's host agent failed — details are in the server logs (journalctl -u jabali-panel / -u jabali-agent)",
    upload_unreachable:
      "The server could not reach the report upload service (enclosed.jabali-panel.com) — check outbound HTTPS/DNS from the server, then retry",
    agent_timeout: "The operation timed out on the server — try again shortly",
  };
  return messages[code] ?? code.replace(/_/g, " ");
}

/**
 * Initiate phpMyAdmin SSO by issuing a redirect token for the given database.
 * Returns the URL to navigate to in the same tab.
 * The URL contains a live credential token and must not be logged.
 */
export async function ssoPhpMyAdmin(
  databaseId: string,
): Promise<{ redirect_url: string }> {
  const resp = await apiClient.post<{ redirect_url: string }>(
    "/sso/phpmyadmin",
    { database_id: databaseId },
  );
  return resp.data;
}

/**
 * Initiate Adminer SSO (M37 Phase 4) — engine-aware bridge for both
 * MariaDB and PostgreSQL. Backend resolves engine from the database
 * row and provisions the appropriate shadow account before minting
 * the token. Returned URL points at /jabali-adminer/?token=...
 */
export async function ssoAdminer(
  databaseId: string,
): Promise<{ redirect_url: string }> {
  const resp = await apiClient.post<{ redirect_url: string }>(
    "/sso/adminer",
    { database_id: databaseId },
  );
  return resp.data;
}

/**
 * Download a one-shot SQL dump of the given database (GH #1045).
 * The backend writes the dump to a scratch file and streams it, so a
 * large database can take far longer than the axios 15s default —
 * timeout: 0 opts this request out. The filename is recovered from the
 * server's RFC 6266 Content-Disposition so the save-as matches what a
 * direct navigation download would produce.
 */
export async function downloadDatabaseBackup(
  databaseId: string,
): Promise<{ blob: Blob; filename: string }> {
  const resp = await apiClient.get<Blob>(`/databases/${databaseId}/backup`, {
    responseType: "blob",
    timeout: 0,
  });
  const cd = resp.headers["content-disposition"] as string | undefined;
  const m = cd?.match(/filename\*?=(?:UTF-8''|")?([^";]+)/i);
  const filename = m
    ? decodeURIComponent(m[1].replace(/"$/, ""))
    : "database.sql";
  return { blob: resp.data, filename };
}

/**
 * Progress snapshot for a database restore-from-file upload (GH #1044).
 * `frac` is 0..1 of the upload; `rate`/`estimated` come from axios's
 * AxiosProgressEvent (bytes/sec and seconds-remaining) when available.
 * Progress covers only the upload leg — once the body is sent the server
 * runs the restore synchronously and reports no further events, so the
 * caller shows an indeterminate "Restoring…" state after frac reaches 1.
 */
export interface RestoreUploadProgress {
  frac: number;
  loaded: number;
  total: number;
  rate?: number;
  estimated?: number;
}

/**
 * Restore a single database from an uploaded .sql dump (GH #1045).
 * Multipart field name is "file" (server contract; the app cap is the
 * admin `upload_max_size_mb`). The restore runs synchronously server-side
 * (the agent loads the dump), so the request uses no client timeout; the
 * server clears its own read/write deadlines for this route (GH #1044).
 * `onProgress` fires during the upload leg for the progress modal.
 */
export async function restoreDatabaseUpload(
  databaseId: string,
  file: File,
  onProgress?: (p: RestoreUploadProgress) => void,
): Promise<void> {
  const form = new FormData();
  form.append("file", file);
  await apiClient.post(`/databases/${databaseId}/restore`, form, {
    timeout: 0,
    onUploadProgress: (e) => {
      if (!onProgress) return;
      const total = e.total ?? file.size;
      onProgress({
        frac: total > 0 ? Math.min(1, e.loaded / total) : 0,
        loaded: e.loaded,
        total,
        rate: e.rate,
        estimated: e.estimated,
      });
    },
  });
}

// === PHP Settings API ===

export interface DomainPHPSettings {
  php_pool_id?: string | null;
  php_version?: string | null;
  php_memory_limit?: string | null;
  php_upload_max_filesize?: string | null;
  php_post_max_size?: string | null;
  php_max_input_vars?: number | null;
  php_max_execution_time?: number | null;
  php_max_input_time?: number | null;
}

export interface UpdateDomainPHPSettingsRequest {
  php_memory_limit?: string | null;
  php_upload_max_filesize?: string | null;
  php_post_max_size?: string | null;
  php_max_input_vars?: number | null;
  php_max_execution_time?: number | null;
  php_max_input_time?: number | null;
}

/**
 * Fetch PHP settings for a specific domain
 */
export async function getDomainPHPSettings(
  domainId: string,
): Promise<DomainPHPSettings> {
  const resp = await apiClient.get<DomainPHPSettings>(
    `/domains/${domainId}/php-settings`,
  );
  return resp.data;
}

/**
 * Update PHP settings for a specific domain
 */
export async function updateDomainPHPSettings(
  domainId: string,
  settings: UpdateDomainPHPSettingsRequest,
): Promise<void> {
  await apiClient.patch(`/domains/${domainId}/php-settings`, settings);
}

// === SSH Keys API ===

export interface SSHKey {
  id: string;
  name: string;
  fingerprint: string;
  created_at: string;
}

export interface SSHKeyListResponse {
  items: SSHKey[];
}

/**
 * List the user's SSH keys
 */
export async function listSSHKeys(): Promise<SSHKeyListResponse> {
  const resp = await apiClient.get<SSHKeyListResponse>("/ssh-keys");
  return resp.data;
}

/**
 * Create a new SSH key for the user
 */
export async function createSSHKey(body: {
  name: string;
  public_key: string;
}): Promise<SSHKey> {
  const resp = await apiClient.post<SSHKey>("/ssh-keys", body);
  return resp.data;
}

/**
 * Delete an SSH key by ID
 */
export async function deleteSSHKey(id: string): Promise<void> {
  await apiClient.delete(`/ssh-keys/${id}`);
}

// --- FTP/SFTP subaccounts (GH #1053) ---

export interface FtpAccount {
  id: string;
  user_id: string;
  username: string;
  home_path: string;
  ftp_access: boolean;
  sftp_access: boolean;
  // GH #1146: WebDAV access (the 3rd protocol). Served at <origin>/dav/.
  webdav_access: boolean;
  is_enabled: boolean;
  // GH #1145: true = separate-uid jailed (kernel-isolated); false/absent =
  // legacy shared-access alias.
  isolated?: boolean;
  quota_mb?: number;
  created_at?: string;
  updated_at?: string;
}

export interface FtpAccountListResponse {
  data: FtpAccount[];
  total: number;
}

export async function listFtpAccounts(): Promise<FtpAccountListResponse> {
  const resp = await apiClient.get<FtpAccountListResponse>("/me/ftp-accounts");
  return resp.data;
}

export async function createFtpAccount(body: {
  label: string;
  home_path: string;
  password: string;
  ftp_access: boolean;
  sftp_access?: boolean;
  webdav_access?: boolean;
  isolated?: boolean;
  quota_mb?: number;
}): Promise<FtpAccount> {
  const resp = await apiClient.post<FtpAccount>("/me/ftp-accounts", body);
  return resp.data;
}

export async function updateFtpAccount(
  id: string,
  body: { ftp_access?: boolean; sftp_access?: boolean; webdav_access?: boolean; is_enabled?: boolean },
): Promise<FtpAccount> {
  const resp = await apiClient.patch<FtpAccount>(`/me/ftp-accounts/${id}`, body);
  return resp.data;
}

export async function resetFtpAccountPassword(id: string, password: string): Promise<void> {
  await apiClient.post(`/me/ftp-accounts/${id}/password`, { password });
}

export async function deleteFtpAccount(id: string): Promise<void> {
  await apiClient.delete(`/me/ftp-accounts/${id}`);
}

export interface SSHConnection {
  host: string;
  port: number;
  username: string;
  command: string;
}

/**
 * Fetch the caller's SSH connection details (host, port, username, command).
 * Returns 409 with error "no_linux_account" for accounts without a Linux user
 * (e.g., admins) — callers should treat that as "SSH not applicable".
 */
export async function getSSHConnection(): Promise<SSHConnection> {
  const resp = await apiClient.get<SSHConnection>("/me/ssh-connection");
  return resp.data;
}

// === SSH TCP forwarding opt-in (GH #1229) ===
// Off by default keeps the JAB-352 lockdown; opting a user in gives them
// loopback-only forwarding (enough for VS Code Remote-SSH). Admin-only to flip;
// a tenant can read their own status. ssh_enabled reflects the package grant —
// the toggle is moot for a user with no SSH shell.
export interface SSHForwardingStatus {
  ssh_forwarding_enabled: boolean;
  ssh_enabled: boolean;
}

/** The caller's own SSH-forwarding status (read-only for tenants). */
export async function getMySSHForwarding(): Promise<SSHForwardingStatus> {
  const resp = await apiClient.get<SSHForwardingStatus>("/me/ssh-forwarding");
  return resp.data;
}

/** Admin: read a user's SSH-forwarding status. */
export async function getUserSSHForwarding(userId: string): Promise<SSHForwardingStatus> {
  const resp = await apiClient.get<SSHForwardingStatus>(`/admin/users/${userId}/ssh-forwarding`);
  return resp.data;
}

/** Admin: enable/disable a user's SSH forwarding. Returns the new status. */
export async function setUserSSHForwarding(
  userId: string,
  enabled: boolean,
): Promise<SSHForwardingStatus> {
  const resp = await apiClient.post<SSHForwardingStatus>(
    `/admin/users/${userId}/ssh-forwarding`,
    { enabled },
  );
  return resp.data;
}

// === Cron Jobs API ===

export interface CronJob {
  id: string;
  user_id: string;
  name: string;
  command: string;
  schedule: string;
  enabled: boolean;
  last_run_at: string | null;
  last_exit_code: number | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
}

export interface CronJobListResponse {
  items: CronJob[];
}

export interface CronRunNowResponse {
  exit_code: number;
  stdout: string;
  stderr: string;
}

export interface CronLogResponse {
  log: string;
  lines: number;
}

/**
 * List the user's cron jobs
 */
export async function listCronJobs(): Promise<CronJobListResponse> {
  const resp = await apiClient.get<CronJobListResponse>("/cron");
  return resp.data;
}

export interface AdminCronJob extends CronJob {
  username: string;
}
export interface AdminCronJobListResponse {
  items: AdminCronJob[];
}
/**
 * Admin: list every cron job on the system. Username is denormalised
 * server-side so the table can show the owner without an N+1 fetch.
 * Requires claims.IsAdmin; 403 otherwise.
 */
export async function listAdminCronJobs(): Promise<AdminCronJobListResponse> {
  const resp = await apiClient.get<AdminCronJobListResponse>("/admin/cron");
  return resp.data;
}

/**
 * Create a new cron job
 */
export async function createCronJob(body: {
  name: string;
  command: string;
  schedule: string;
  enabled?: boolean;
  /** Admin-only override: create the cron job under a different tenant's UserID. Ignored when caller is not admin. */
  user_id?: string;
  /** Admin-only: when true, the cron runs as root via a system-scoped systemd timer. Ignored for tenants. */
  run_as_root?: boolean;
}): Promise<CronJob> {
  const resp = await apiClient.post<CronJob>("/cron", body);
  return resp.data;
}

/**
 * Get a single cron job
 */
export async function getCronJob(id: string): Promise<CronJob> {
  const resp = await apiClient.get<CronJob>(`/cron/${id}`);
  return resp.data;
}

/**
 * Update a cron job
 */
export async function updateCronJob(
  id: string,
  body: {
    name?: string;
    command?: string;
    schedule?: string;
    enabled?: boolean;
  },
): Promise<CronJob> {
  const resp = await apiClient.patch<CronJob>(`/cron/${id}`, body);
  return resp.data;
}

/**
 * Delete a cron job
 */
export async function deleteCronJob(id: string): Promise<void> {
  await apiClient.delete(`/cron/${id}`);
}

/**
 * Run a cron job immediately
 */
export async function runCronJobNow(id: string): Promise<CronRunNowResponse> {
  const resp = await apiClient.post<CronRunNowResponse>(`/cron/${id}/run-now`);
  return resp.data;
}

/**
 * Get the log for a cron job
 */
export async function getCronJobLog(
  id: string,
  lines?: number,
): Promise<CronLogResponse> {
  const url = lines ? `/cron/${id}/log?lines=${lines}` : `/cron/${id}/log`;
  const resp = await apiClient.get<CronLogResponse>(url);
  return resp.data;
}

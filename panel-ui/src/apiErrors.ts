// Centralised axios error extractor. Backends return JSON shaped like
//   { status: "error", error: "<code>", detail: "<human msg>", stderr?: "..." }
// (envelope used across panel-api handlers). Without this helper, axios
// surfaces a generic "Request failed with status code 502" message that
// hides the actual cause — operators see "Test failed" with no idea
// whether it's bad creds, missing binary, or network.
import axios, { AxiosError } from "axios";

interface ApiErrorBody {
  status?: string;
  error?: string;
  detail?: string;
  stderr?: string;
  message?: string;
}

/**
 * Extracts the most informative human-readable message from an unknown
 * thrown value. Order of preference:
 *   1. backend's `detail` field
 *   2. backend's `error` code (uppercased to look like a constant)
 *   3. backend's `message` field
 *   4. backend's `stderr` (last 200 chars — restic / shell output)
 *   5. axios's HTTP status text
 *   6. native Error.message
 *   7. fallback string
 *
 * @param err   the value caught in a try/catch
 * @param fallback  used when err carries no useful info
 */
/**
 * isPanelRestartError: the panel isn't answering for a moment, rather than
 * refusing the request. nginx in front of it answers 502 while jabali-panel
 * restarts (it does after its own certificate is issued or regenerated), 504
 * when the panel is slow to come back, and a connection can drop while nginx
 * reloads. A query that runs across such a restart retries these for longer
 * than an ordinary error.
 */
export function isPanelRestartError(err: unknown): boolean {
  if (!axios.isAxiosError(err)) return false;
  const status = err.response?.status;
  if (status === 502 || status === 504) return true;
  return !err.response && err.code === "ERR_NETWORK";
}

export function extractApiError(err: unknown, fallback = "request failed"): string {
  if (axios.isAxiosError(err)) {
    const ax = err as AxiosError<ApiErrorBody>;
    const body = ax.response?.data;
    if (body && typeof body === "object") {
      const parts: string[] = [];
      if (body.detail) parts.push(body.detail);
      else if (body.error) parts.push(body.error);
      else if (body.message) parts.push(body.message);
      if (body.stderr) {
        const s = body.stderr.trim();
        if (s) parts.push(`stderr: ${s.slice(-200)}`);
      }
      if (parts.length > 0) return parts.join(" — ");
    }
    if (ax.response?.status) {
      return `HTTP ${ax.response.status} ${ax.response.statusText || ""}`.trim();
    }
    if (ax.message) return ax.message;
  }
  if (err instanceof Error && err.message) return err.message;
  if (typeof err === "string" && err) return err;
  return fallback;
}

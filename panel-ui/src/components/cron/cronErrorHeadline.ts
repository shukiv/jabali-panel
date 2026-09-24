// Single source of truth for turning a cron validation_failed API response into
// a short, user-friendly headline plus the form field it belongs to. Both the
// tenant CreateCronModal and the admin AdminCreateCronModal render through this,
// so the two doors never drift (the same play CronCommandHelp made for the
// command-restriction copy — GH #1686 items 3+4).
//
// The keys are the cronvalidate ErrCode* constants (internal/cronvalidate:
// cron.go + httptrigger.go) that the API surfaces as `code` on a
// validation_failed response. Keep this map in sync with that code set; the
// backend guard TestMapCronopsErr_SurfacesValidationCode pins that `code` is
// actually sent, and cronErrorHeadline.test.ts pins that every code has an
// entry here.

export interface CronErrorData {
  error?: string;
  field?: string;
  code?: string;
  detail?: string;
}

export type CronErrorField = "command" | "schedule" | "name";

// Messages are audience-neutral on purpose: the same map serves the tenant door
// and the admin root/tenant door, so wording must be true for a tenant account
// AND a root cron (e.g. no "inside your account" — the root containment root is
// /root or the admin's own docroots).
export const CRON_ERROR_HEADLINES: Record<string, string> = {
  // Command + general (internal/cronvalidate/cron.go)
  empty: "This field cannot be empty",
  too_long: "Command is too long (max 1024 bytes)",
  binary_not_allowed: "Command must start with wp, php, python, or node",
  metachar_reject: "Shell metacharacters and control characters are not allowed in the command",
  bad_path_arg: "Invalid path — use an absolute path with no traversal, inside an allowed directory",
  invalid_name: "Invalid name — remove control characters",
  // Schedule (internal/cronvalidate/cron.go)
  bad_schedule_syntax: "Invalid schedule — use a 5-field cron expression",
  schedule_too_frequent: "Schedule is too frequent — the minimum interval is 1 minute",
  // HTTP trigger, curl/wget (internal/cronvalidate/httptrigger.go)
  http_not_curl_wget: "HTTP triggers must start with curl or wget",
  http_no_url: "No http(s) URL found in the command",
  http_multiple_urls: "Only one URL is allowed",
  http_bad_url: "The URL could not be parsed",
  http_bad_scheme: "The URL must use http or https",
  http_userinfo: "The URL must not contain a username or password",
  http_foreign_host: "The URL host must be one of your own domains",
  http_bad_port: "Only ports 80 and 443 are allowed",
  http_dangerous_flag: "That curl/wget flag is not allowed — output is only permitted to /dev/null",
  http_unexpected_arg: "Unexpected argument in the curl/wget command",
};

// cronErrorHeadline maps a validation_failed response body to the field to flag
// and the headline to show. `field` comes from the API first (it is always set
// on validation_failed); the detail/code heuristics are a fallback for older
// responses that omit it. All http_* codes are command-field errors.
export function cronErrorHeadline(
  data: CronErrorData | undefined,
  fallbackMessage = "Failed to save cron job",
): { field?: CronErrorField; headline: string } {
  const d = data ?? {};
  const detail = d.detail ?? fallbackMessage;
  const code = d.code ?? d.error;
  const headline = (code && CRON_ERROR_HEADLINES[code]) || detail;

  let field: CronErrorField | undefined;
  if (d.field === "command" || d.field === "schedule" || d.field === "name") {
    field = d.field;
  } else if ((code && code.startsWith("http_")) || /command|metachar|binary|path|traversal/i.test(detail)) {
    field = "command";
  } else if (/schedule/i.test(detail)) {
    field = "schedule";
  }

  return { field, headline };
}

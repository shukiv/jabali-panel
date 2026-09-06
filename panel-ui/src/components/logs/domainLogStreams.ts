// domainLogStreams.ts — the audience-neutral request-shaping pieces of the
// per-domain log viewer (JAB-296). Extracted out of the admin DomainLogsTab
// and the tenant UserLogsPage so the "which log types exist", "how a stream
// request is shaped", and "what an aggregate row is" decisions live in one
// place with unit tests, instead of being copy-pasted module-locals in two
// shells that drifted independently.

export type LogType = "access" | "error" | "goaccess";

// A row in the domain table. `aggregate` marks the synthetic "All Domains"
// row that only the admin surface prepends — see ALL_DOMAINS_ROW. Tenant rows
// never carry it, which is how the tenant scope is structurally barred from
// ever asking for an aggregate stream (see buildLogStreamPayload / AC2).
export interface DomainLogRow {
  id: string;
  name: string;
  status: string;
  aggregate?: boolean;
}

// Modal titles, keyed by log type. Kept verbatim from the two shells so the
// consolidated copy does not shift under either page (AC6).
export const LOG_STREAM_TITLES: Record<LogType, string> = {
  access: "Access Log Stream",
  error: "Error Log Stream",
  goaccess: "GoAccess Real-Time Dashboard",
};

// Row-action labels, keyed by log type.
export const LOG_STREAM_LABELS: Record<LogType, string> = {
  access: "Access Log",
  error: "Error Log",
  goaccess: "Real Time",
};

// The synthetic aggregate row. Admin prepends it to stream logs across every
// domain at once; the empty id is never sent as a domain identity — the
// aggregate request omits domain_id entirely (see buildLogStreamPayload).
export const ALL_DOMAINS_ROW: DomainLogRow = {
  id: "",
  name: "All Domains",
  status: "system",
  aggregate: true,
};

export const isAggregateRow = (row: DomainLogRow): boolean =>
  row.aggregate === true;

// The POST /logs/access body. An aggregate request is the ABSENCE of
// domain_id, never an empty string — so it is unrepresentable unless the
// caller explicitly passes no domainId at all.
export type LogStreamPayload =
  | { log_type: LogType }
  | { log_type: LogType; domain_id: string };

// buildLogStreamPayload shapes the create-stream request.
//
// AC1: an aggregate request (domainId === undefined) omits domain identity;
// a per-domain request (admin row OR any tenant request) includes it.
//
// AC2: the aggregate is keyed on `undefined`, NOT on a falsy id. A caller that
// passes any string — including "" — gets a domain_id in the body, so the
// tenant surface (whose column ctx always calls with a row id) can never
// accidentally emit an aggregate request. Only an explicit no-arg call does.
export function buildLogStreamPayload(
  logType: LogType,
  domainId?: string,
): LogStreamPayload {
  return domainId === undefined
    ? { log_type: logType }
    : { log_type: logType, domain_id: domainId };
}

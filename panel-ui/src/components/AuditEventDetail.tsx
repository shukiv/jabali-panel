// AuditEventDetail — shared display helpers for the M49 audit views
// (ADR-0106). Used by both the admin "Audit Log" (AdminAuditList) and
// the user "Account Activity" (AccountActivity) so the two stay in
// lockstep: same timestamp formatting, same actor rendering, and the
// same expandable detail row (click a row to see everything recorded
// for that event, inline — no modal).
//
// Honest scope note: the recorder NEVER captures request bodies or
// secrets (ADR-0106). So the detail panel shows exactly what is on the
// row — method+route, target, actor, result, request id and structured
// meta. A friendly verb ("Create mailbox") is derived from the route
// template; the specific resource value (e.g. the mailbox address)
// would require typed domain emitters and is intentionally NOT faked.
import type { ReactNode } from "react";
import { Descriptions, Tag, Typography } from "antd";

import { useServerStatus } from "../hooks/useServerStatus";

// Superset row shape covering both /admin/audit (full) and
// /me/activity (no actor_user_id / request_id). Everything past the
// always-present core is optional so one component serves both feeds.
export type AuditRow = {
  id: string;
  ts: string;
  actor_user_id?: string;
  actor_name?: string;
  actor_kind: string;
  subject_user_id?: string;
  subject_name?: string;
  action: string;
  target_type: string;
  target_id: string;
  result: "ok" | "denied" | "error";
  source_ip?: string;
  request_id?: string;
  meta?: unknown;
};

export const resultTag = (r: AuditRow["result"]) => (
  <Tag color={r === "ok" ? "green" : r === "denied" ? "gold" : "red"}>{r}</Tag>
);

export const dash = <Typography.Text type="secondary">—</Typography.Text>;

// The audit store keeps UTC; operators want their server's wall clock
// (Server Settings → Timezone, surfaced by the admin server-status
// host slice). Admin-only endpoint, so the user feed falls back to the
// viewer's browser tz — either way the column header names the tz in
// use, so the rendered time is never ambiguous.
export function useServerTz(): string {
  const { data } = useServerStatus();
  return data?.host?.timezone || browserTz();
}

export function browserTz(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

// "YYYY-MM-DD HH:MM:SS" rendered in `tz`. Falls back to the raw ISO
// string if the timestamp or the tz is unparseable (never throws into
// a table cell).
export function fmtTSInTz(ts: string, tz: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  try {
    return new Intl.DateTimeFormat("en-CA", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
      timeZone: tz,
    })
      .format(d)
      .replace(", ", " ");
  } catch {
    return d.toISOString().replace("T", " ").replace(/\.\d+Z$/, "Z");
  }
}

const VERB: Record<string, string> = {
  POST: "Create",
  PUT: "Update",
  PATCH: "Update",
  DELETE: "Delete",
  GET: "View",
};

// Path segments that end in "s" without being plurals.
const NOT_PLURAL = new Set(["dns", "mta-sts"]);

function singularize(noun: string): string {
  if (NOT_PLURAL.has(noun)) return noun;
  if (noun.endsWith("ies")) return `${noun.slice(0, -3)}y`;
  // "-es" is the plural ending only after these (processes, mailboxes,
  // batches, aliases); databases and services drop just the "s".
  if (/(ss|x|z|ch|sh|ias)es$/.test(noun)) return noun.slice(0, -2);
  if (noun.endsWith("s") && !noun.endsWith("ss")) return noun.slice(0, -1);
  return noun;
}

// Best-effort friendly label from the recorded "METHOD /api/v1/..."
// action. The exact resource value isn't on the row (bodies are never
// recorded), so this is a verb + resource-noun summary; the expanded
// row shows the raw action so nothing is hidden.
export function humanizeAction(r: AuditRow): string {
  const sp = r.action.indexOf(" ");
  if (sp < 0) return r.action;
  const method = r.action.slice(0, sp);
  const path = r.action.slice(sp + 1);
  const verb = VERB[method] ?? method;
  const segs = path
    .split("/")
    .filter((s) => s && s !== "api" && s !== "v1" && !s.startsWith(":"));
  if (segs.length === 0) return r.action;
  const noun = singularize(segs[segs.length - 1]).replace(/-/g, " ");
  return `${verb} ${noun}`;
}

const metaText = (meta: unknown): string | null => {
  if (meta == null) return null;
  try {
    const s = JSON.stringify(meta, null, 2);
    return s && s !== "{}" && s !== "null" ? s : null;
  } catch {
    return null;
  }
};

interface AuditActionLabelProps {
  row: AuditRow;
  // Optional leading badge (the user feed flags admin-initiated rows).
  prefix?: ReactNode;
}

// The Action column cell: the friendly label as plain text. The full
// recorded detail is one click away in the expandable row, not a
// modal — so this stays a quiet, scannable summary.
export function AuditActionLabel({ row, prefix }: AuditActionLabelProps) {
  return (
    <span>
      {prefix}
      {humanizeAction(row)}
    </span>
  );
}

interface AuditDetailProps {
  row: AuditRow;
  tz: string;
}

// expandedRowRender body: every field actually recorded for the event,
// inline beneath its row.
export function AuditDetail({ row, tz }: AuditDetailProps) {
  const meta = metaText(row.meta);
  const actor = row.actor_name || row.actor_user_id || `(${row.actor_kind})`;
  const subject = row.subject_name || row.subject_user_id;

  return (
    <Descriptions column={1} size="small" bordered>
      <Descriptions.Item label="When">
        <code>{fmtTSInTz(row.ts, tz)}</code>{" "}
        <Typography.Text type="secondary">({tz})</Typography.Text>
        <br />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {row.ts} (UTC)
        </Typography.Text>
      </Descriptions.Item>
      <Descriptions.Item label="Result">
        {resultTag(row.result)}
      </Descriptions.Item>
      <Descriptions.Item label="Actor">
        <Tag>{row.actor_kind}</Tag> {actor}
        {row.actor_name && row.actor_user_id ? (
          <>
            {" "}
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {row.actor_user_id}
            </Typography.Text>
          </>
        ) : null}
      </Descriptions.Item>
      {subject ? (
        <Descriptions.Item label="Subject">{subject}</Descriptions.Item>
      ) : null}
      <Descriptions.Item label="Action">
        <code>{row.action}</code>
      </Descriptions.Item>
      <Descriptions.Item label="Target">
        {row.target_type || row.target_id ? (
          <code>
            {row.target_type}/{row.target_id}
          </code>
        ) : (
          dash
        )}
      </Descriptions.Item>
      <Descriptions.Item label="Source IP">
        {row.source_ip ? <code>{row.source_ip}</code> : dash}
      </Descriptions.Item>
      {row.request_id ? (
        <Descriptions.Item label="Request ID">
          <code>{row.request_id}</code>
        </Descriptions.Item>
      ) : null}
      {meta ? (
        <Descriptions.Item label="Meta">
          <pre style={{ margin: 0, whiteSpace: "pre-wrap" }}>{meta}</pre>
        </Descriptions.Item>
      ) : null}
    </Descriptions>
  );
}

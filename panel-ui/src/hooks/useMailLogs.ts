// useMailLogs.ts — M6.5 Step 7 mail log viewer hook (pass-through).

import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../apiClient";

export interface MailLogEntry {
  timestamp: string;
  from: string;
  to: string;
  size: number;
  /** GH #262: delivery outcome — "delivered" | "failed" | "queued". */
  status?: string;
  /** GH #261: Stalwart queue id, for the per-message delivery-trail drawer. */
  queue_id?: string;
}

export interface MailLogsQuery {
  from_date?: string;
  to_date?: string;
  sender?: string;
  recipient?: string;
  limit?: number;
  offset?: number;
  /** GH #1387: narrow to one owned domain (drill-down); ignored if unowned. */
  domain?: string;
}

export function useMailLogs(q: MailLogsQuery) {
  return useQuery({
    queryKey: ["mail_logs", q],
    queryFn: async () => {
      const params = new URLSearchParams();
      if (q.from_date) params.set("from_date", q.from_date);
      if (q.to_date) params.set("to_date", q.to_date);
      if (q.sender) params.set("sender", q.sender);
      if (q.recipient) params.set("recipient", q.recipient);
      if (q.limit) params.set("limit", String(q.limit));
      if (q.offset) params.set("offset", String(q.offset));
      if (q.domain) params.set("domain", q.domain);
      const { data } = await apiClient.get<{
        data: MailLogEntry[];
        total: number;
        page: number;
        page_size: number;
      }>(`/mail/logs?${params.toString()}`);
      return data;
    },
    refetchInterval: 30000,
  });
}

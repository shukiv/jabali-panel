// useAutoresponders.ts — M6.5 Step 3 vacation response hooks.
//
// Wire contract: GET/PUT/DELETE /mailboxes/:mbid/autoresponder
// Verified against panel-api/internal/api/mailbox_autoresponder.go.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../apiClient";

export interface Autoresponder {
  mailbox_id: string;
  enabled: boolean;
  from_date: string | null;
  to_date: string | null;
  subject: string | null;
  text_body: string | null;
  html_body: string | null;
  updated_at: string;
}

export interface AutoresponderInput {
  enabled: boolean;
  from_date?: string | null;
  to_date?: string | null;
  subject?: string | null;
  text_body?: string | null;
  html_body?: string | null;
}

const QK = (mailboxID: string) => ["autoresponder", mailboxID];

export function useAutoresponder(mailboxID: string) {
  return useQuery({
    queryKey: QK(mailboxID),
    queryFn: async () => {
      const { data } = await apiClient.get<Autoresponder>(
        `/mailboxes/${mailboxID}/autoresponder`,
      );
      return data;
    },
    enabled: !!mailboxID,
  });
}

// useMailboxAutoresponders returns, keyed by mailbox id, the autoresponder for
// each mailbox (JAB-370 Selection). It mirrors the mailbox-group-memberships
// bulk sibling: ONE owner-scoped request — GET /mail/autoresponders spanning
// every domain the caller owns (the cross-domain Mailboxes tab) — or the
// per-domain endpoint when the tab is embedded in the Mail Domains drill-down
// (domainId set). This replaced the one-request-per-email-enabled-domain
// fan-out that merged the maps in the browser. Same { <mailbox_id>: ar } shape
// either way. The key sits under the ["autoresponders"] family, so the
// per-mailbox mutations (which bust that whole family) refresh it too.
export function useMailboxAutoresponders(domainId?: string, enabled = true) {
  return useQuery({
    // Bulk (cross-domain) view keys on the literal "me" in the domain slot; the
    // drill-down keys on the domain id. Both live under ["autoresponders",...].
    queryKey: ["autoresponders", "by-domain", domainId ?? "me"],
    queryFn: async () => {
      const path = domainId
        ? `/domains/${domainId}/autoresponders`
        : "/mail/autoresponders";
      const { data } = await apiClient.get<{ data: Record<string, Autoresponder> }>(path);
      return data.data ?? {};
    },
    enabled,
  });
}

export function useUpdateAutoresponder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ mailboxID, input }: { mailboxID: string; input: AutoresponderInput }) => {
      const { data } = await apiClient.put<Autoresponder>(
        `/mailboxes/${mailboxID}/autoresponder`,
        input,
      );
      return data;
    },
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: QK(data.mailbox_id) });
      // GH #240: the Mailboxes tab "Auto replies" column reads a
      // per-domain bulk query; invalidate the whole family so it refreshes.
      qc.invalidateQueries({ queryKey: ["autoresponders"] });
    },
  });
}

export function useDeleteAutoresponder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (mailboxID: string) => {
      await apiClient.delete(`/mailboxes/${mailboxID}/autoresponder`);
    },
    onSuccess: (_void, mailboxID) => {
      qc.invalidateQueries({ queryKey: QK(mailboxID) });
      qc.invalidateQueries({ queryKey: ["autoresponders"] });
    },
  });
}

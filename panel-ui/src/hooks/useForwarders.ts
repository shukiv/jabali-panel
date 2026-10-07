// useForwarders.ts — M6.5 Step 5 forwarder hooks.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../apiClient";
import { fetchAllPages } from "../lib/fetchAllPages";

export interface Forwarder {
  id: string;
  mailbox_id: string;
  mailbox_email: string;
  domain_id: string;
  domain_name: string;
  type: "alias" | "external";
  local_part?: string;
  target: string;
  keep_copy: boolean;
  enabled: boolean;
  created_at: string;
  // Set on create when the forwarder was saved but the mail server did not
  // accept it, so it is not forwarding yet.
  warning?: { code: string; detail: string };
}

const QK = ["forwarders"];

// useForwarders lists the account's forwarders, every page of them. With a
// domainId it lists that mail domain's only, as its Forwarders tab shows
// (GH #1997). The mutations invalidate both through the shared QK prefix.
export function useForwarders(domainId?: string) {
  return useQuery({
    queryKey: [...QK, domainId ?? "all"],
    queryFn: () =>
      fetchAllPages<Forwarder>("/mail/forwarders", domainId ? { domain_id: domainId } : {}),
  });
}

export function useCreateForwarder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      mailboxID,
      type,
      localPart,
      target,
      keepCopy,
    }: {
      mailboxID: string;
      type: "alias" | "external";
      localPart?: string;
      target: string;
      keepCopy?: boolean;
    }) => {
      const { data } = await apiClient.post<Forwarder>(
        `/mailboxes/${mailboxID}/forwarders`,
        { type, local_part: localPart, target, keep_copy: keepCopy ?? false },
      );
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: QK }),
  });
}

export function useDeleteForwarder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (forwarderID: string) => {
      await apiClient.delete(`/forwarders/${forwarderID}`);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: QK }),
  });
}

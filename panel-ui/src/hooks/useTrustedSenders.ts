// useTrustedSenders.ts — GH #2017: the senders a mailbox trusts. Mail from
// one of them is not treated as spam when it passes SPF or DMARC. Backed by
// /mailboxes/:mbid/trusted-senders (see mailbox_trusted_senders.go).

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../apiClient";

export interface TrustedSender {
  id: string;
  address: string;
  created_at: string;
}

export interface TrustedSenderList {
  data: TrustedSender[];
  total: number;
  max: number;
}

// AddedTrustedSender carries a warning when the sender was saved but the mail
// server has not taken it yet; the panel retries.
export interface AddedTrustedSender extends TrustedSender {
  warning?: string;
}

const qk = (mailboxId: string) => ["trusted-senders", mailboxId];

export function useTrustedSenders(mailboxId: string | undefined) {
  return useQuery({
    queryKey: qk(mailboxId ?? ""),
    enabled: !!mailboxId,
    queryFn: async () => {
      const { data } = await apiClient.get<TrustedSenderList>(`/mailboxes/${mailboxId}/trusted-senders`);
      return { data: data.data ?? [], total: data.total ?? 0, max: data.max ?? 500 };
    },
  });
}

export function useAddTrustedSender() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ mailboxId, address }: { mailboxId: string; address: string }) => {
      const { data } = await apiClient.post<AddedTrustedSender>(`/mailboxes/${mailboxId}/trusted-senders`, {
        address,
      });
      return data;
    },
    onSuccess: (_d, v) => qc.invalidateQueries({ queryKey: qk(v.mailboxId) }),
  });
}

export function useDeleteTrustedSender() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ mailboxId, id }: { mailboxId: string; id: string }) => {
      await apiClient.delete(`/mailboxes/${mailboxId}/trusted-senders/${id}`);
    },
    onSuccess: (_d, v) => qc.invalidateQueries({ queryKey: qk(v.mailboxId) }),
  });
}

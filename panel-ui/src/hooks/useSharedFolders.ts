// useSharedFolders.ts — M6.5 Step 4 mailbox share hooks.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../apiClient";
import { fetchAllPages } from "../lib/fetchAllPages";

export interface Rights {
  mayRead?: boolean;
  mayAddItems?: boolean;
  mayRemoveItems?: boolean;
  mayCreateChild?: boolean;
  mayRename?: boolean;
  mayDelete?: boolean;
  mayAdmin?: boolean;
  maySubmit?: boolean;
}

export interface MailboxShare {
  id: string;
  owner_mailbox_id: string;
  owner_mailbox_email?: string;
  shared_with_mailbox_id: string;
  shared_with_mailbox_email?: string;
  rights: Rights;
  created_at: string;
  // Set on create when the share was saved but the mail server did not
  // accept it yet (the panel retries it).
  warning?: { code: string; detail: string };
}

const QK_ALL = ["mail_shares", "all"];

// useAllShares lists the account's shares, every page of them. With a
// domainId it lists the shares that involve that mail domain, from or to
// it, as its Shared Folders tab shows (GH #1997). The mutations invalidate
// both through the QK_ALL prefix.
export function useAllShares(domainId?: string) {
  return useQuery({
    queryKey: [...QK_ALL, domainId ?? "account"],
    queryFn: () =>
      fetchAllPages<MailboxShare>("/mail/shares", domainId ? { domain_id: domainId } : {}),
  });
}

export function useCreateShare() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      ownerMailboxID,
      sharedWithMailboxID,
      rights,
    }: {
      ownerMailboxID: string;
      sharedWithMailboxID: string;
      rights: Rights;
    }) => {
      const { data } = await apiClient.post<MailboxShare>(
        `/mailboxes/${ownerMailboxID}/shares`,
        { shared_with_mailbox_id: sharedWithMailboxID, rights },
      );
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: QK_ALL }),
  });
}

export function useDeleteShare() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      ownerMailboxID,
      shareID,
    }: {
      ownerMailboxID: string;
      shareID: string;
    }) => {
      await apiClient.delete(`/mailboxes/${ownerMailboxID}/shares/${shareID}`);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: QK_ALL }),
  });
}

// useMailboxes.ts — M6 email-domain + mailbox hooks.
//
// Thin wrappers around apiClient that TanStack-query-cache the three
// routes the UI cares about:
//   - GET    /domains/:id/email                → useDomainEmail
//   - POST   /domains/:id/email                → useEnableDomainEmail
//   - DELETE /domains/:id/email                → useDisableDomainEmail
//   - GET    /domains/:id/mailboxes?page=…     → useMailboxes
//   - POST   /domains/:id/mailboxes            → useCreateMailbox
//   - DELETE /mailboxes/:id                    → useDeleteMailbox
//   - POST   /mailboxes/:id/rotate-password    → useRotateMailboxPassword
//   - PATCH  /mailboxes/:id                    → useUpdateMailboxQuota
//
// The generic useListQuery/useOneQuery hooks in useQueries.ts don't
// quite fit here because mailboxes are nested under a domain path
// (/domains/:id/mailboxes, not /mailboxes), so we roll dedicated hooks
// that still plug into the same ["list", resource] cache convention.
//
// Wire-contract note (per `feedback_verify_wire_contract`): the panel-
// API envelope is {data, total, page, page_size} (see
// panel-api/internal/api/mailboxes.go:177). We unwrap it to
// {items, total} on the consumer side, matching useQueries.ts.
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";

import { apiClient } from "../apiClient";
import type { ListParams, ListResponse } from "./useQueries";

// ---------------------------------------------------------------------------
// Types — mirror panel-api response shapes.
// ---------------------------------------------------------------------------

export interface DomainEmailDNSHint {
  purpose: string;
  name: string;
  type: string;
  value: string;
  status?: string;
}

export interface DomainEmailState {
  domain_id: string;
  domain_name: string;
  email_enabled: boolean;
  dkim_selector?: string;
  dkim_public_key?: string;
  email_enabled_at?: string;
  records: DomainEmailDNSHint[];
  /**
   * Operator-actionable advisories from the last enable/get. Populated
   * when a user-edited DNS row blocks an M6 insert — UI renders each
   * entry as a list item in a warning alert above the records table.
   */
  warnings?: string[];
}

export interface Mailbox {
  id: string;
  domain_id: string;
  email: string;
  display_name: string;
  quota_bytes: number;
  is_disabled: boolean;
  // send_only (GH #371): authenticates for SMTP submission but never
  // receives or stores mail.
  send_only: boolean;
  last_usage_bytes: number;
  last_usage_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface CreateMailboxInput {
  local_part: string;
  display_name?: string;
  password?: string;
  quota_bytes?: number;
  send_only?: boolean;
}

export interface CreateMailboxResponse {
  id: string;
  email: string;
  quota_bytes: number;
  display_name?: string;
  send_only?: boolean;
  // Present only when caller didn't supply a password — reveal-once.
  password?: string;
}

// Wire envelope — panel-api always returns `data`, we unwrap.
interface RawListEnvelope<T> {
  data?: T[];
  total?: number;
  page?: number;
  page_size?: number;
}

function toQueryString(params: ListParams): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue;
    const wireKey = k === "pageSize" ? "page_size" : k;
    sp.set(wireKey, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}

// ---------------------------------------------------------------------------
// Domain email (enable/disable state)
// ---------------------------------------------------------------------------

export function useDomainEmail(
  domainId: string | undefined,
): UseQueryResult<DomainEmailState> {
  return useQuery({
    queryKey: ["one", "domain-email", domainId],
    queryFn: async () => {
      const { data } = await apiClient.get<DomainEmailState>(
        `/domains/${domainId}/email`,
      );
      return data;
    },
    enabled: !!domainId,
  });
}

export function useEnableDomainEmail(): UseMutationResult<
  DomainEmailState,
  unknown,
  { domainId: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ domainId }) => {
      const { data } = await apiClient.post<DomainEmailState>(
        `/domains/${domainId}/email`,
      );
      return data;
    },
    onSuccess: (_data, { domainId }) => {
      // Invalidate the email state AND the domain row itself (the
      // domain's email_enabled field is read by the DomainEdit tab
      // switcher).
      qc.invalidateQueries({ queryKey: ["one", "domain-email", domainId] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      // Also bust the mailbox list — creating mailboxes requires
      // email_enabled, so the UI gate depends on this.
      qc.invalidateQueries({ queryKey: ["list", "mailboxes", domainId] });
    },
  });
}

export function useDisableDomainEmail(): UseMutationResult<
  void,
  unknown,
  { domainId: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ domainId }) => {
      await apiClient.delete(`/domains/${domainId}/email`);
    },
    onSuccess: (_data, { domainId }) => {
      qc.invalidateQueries({ queryKey: ["one", "domain-email", domainId] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
    },
  });
}

// ---------------------------------------------------------------------------
// Mailboxes (nested under /domains/:id/mailboxes on the wire)
// ---------------------------------------------------------------------------

export type UseMailboxesResult = UseQueryResult<ListResponse<Mailbox>> & {
  items: Mailbox[];
  total: number;
};

export function useMailboxes({
  domainId,
  params = {},
  enabled = true,
}: {
  domainId: string | undefined;
  params?: ListParams;
  enabled?: boolean;
}): UseMailboxesResult {
  const q = useQuery({
    queryKey: ["list", "mailboxes", domainId, params],
    queryFn: async () => {
      const { data: raw } = await apiClient.get<RawListEnvelope<Mailbox>>(
        `/domains/${domainId}/mailboxes${toQueryString(params)}`,
      );
      return {
        items: raw.data ?? [],
        total: raw.total ?? 0,
      };
    },
    enabled: enabled && !!domainId,
  });
  return {
    ...q,
    items: q.data?.items ?? [],
    total: q.data?.total ?? 0,
  };
}

export function useCreateMailbox(): UseMutationResult<
  CreateMailboxResponse,
  unknown,
  { domainId: string; input: CreateMailboxInput }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ domainId, input }) => {
      const { data } = await apiClient.post<CreateMailboxResponse>(
        `/domains/${domainId}/mailboxes`,
        input,
      );
      return data;
    },
    onSuccess: (_data, { domainId }) => {
      qc.invalidateQueries({ queryKey: ["list", "mailboxes", domainId] });
      qc.invalidateQueries({ queryKey: ["admin", "mailboxes"] });
    },
  });
}

export function useDeleteMailbox(): UseMutationResult<
  void,
  unknown,
  { id: string; domainId: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id }) => {
      await apiClient.delete(`/mailboxes/${id}`);
    },
    onSuccess: (_data, { domainId }) => {
      qc.invalidateQueries({ queryKey: ["list", "mailboxes", domainId] });
      qc.invalidateQueries({ queryKey: ["admin", "mailboxes"] });
    },
  });
}

export function useRotateMailboxPassword(): UseMutationResult<
  { password?: string },
  unknown,
  { id: string; new_password?: string }
> {
  return useMutation({
    mutationFn: async ({ id, new_password }) => {
      const { data } = await apiClient.post<{ password?: string }>(
        `/mailboxes/${id}/rotate-password`,
        { new_password: new_password ?? "" },
      );
      return data;
    },
  });
}

export interface AdminMailbox extends Mailbox {
  domain_name: string;
  owner_user_id: string;
  user_username: string;
}

// useAdminMailboxes lists every mailbox on the server (admin-only) for the
// server-wide Mail tab.
export function useAdminMailboxes(): UseQueryResult<AdminMailbox[]> {
  return useQuery({
    queryKey: ["admin", "mailboxes"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data?: AdminMailbox[] }>(
        "/admin/mailboxes",
      );
      return data.data ?? [];
    },
  });
}

// useUpdateMailbox is the general partial-update PATCH (GH #197 + admin
// Mail tab): any provided field (display_name / quota_bytes / is_disabled)
// is updated, the rest left unchanged.
export function useUpdateMailbox(): UseMutationResult<
  Mailbox,
  unknown,
  {
    id: string;
    domainId?: string;
    display_name?: string;
    quota_bytes?: number;
    is_disabled?: boolean;
    send_only?: boolean;
  }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, display_name, quota_bytes, is_disabled, send_only }) => {
      const body: Record<string, unknown> = {};
      if (display_name !== undefined) body.display_name = display_name;
      if (quota_bytes !== undefined) body.quota_bytes = quota_bytes;
      if (is_disabled !== undefined) body.is_disabled = is_disabled;
      if (send_only !== undefined) body.send_only = send_only;
      const { data } = await apiClient.patch<Mailbox>(`/mailboxes/${id}`, body);
      return data;
    },
    onSuccess: (_data, { domainId }) => {
      if (domainId) {
        qc.invalidateQueries({ queryKey: ["list", "mailboxes", domainId] });
      }
      qc.invalidateQueries({ queryKey: ["list", "mailboxes"] });
      qc.invalidateQueries({ queryKey: ["admin", "mailboxes"] });
    },
  });
}

export function useUpdateMailboxQuota(): UseMutationResult<
  Mailbox,
  unknown,
  { id: string; domainId: string; quota_bytes: number }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, quota_bytes }) => {
      const { data } = await apiClient.patch<Mailbox>(`/mailboxes/${id}`, {
        quota_bytes,
      });
      return data;
    },
    onSuccess: (_data, { domainId }) => {
      qc.invalidateQueries({ queryKey: ["list", "mailboxes", domainId] });
    },
  });
}

// Webmail SSO (M6 Step 8 Phase B): panel mints a one-shot URL that
// lands on mail.<domain>/sso/webmail, which forwards the user into
// Bulwark with a session cookie set. The mutation returns the URL;
// the caller pops a new tab to it — opening synchronously from the
// click handler is important so browsers don't block the popup.
export interface MailboxSSOResponse {
  url: string;
  mail_host: string;
  expires_in: number;
}

export function useMintMailboxSSO(): UseMutationResult<
  MailboxSSOResponse,
  unknown,
  { id: string }
> {
  return useMutation({
    mutationFn: async ({ id }) => {
      const { data } = await apiClient.post<MailboxSSOResponse>(
        `/mailboxes/${id}/sso`,
      );
      return data;
    },
  });
}

// useMailGroups.ts — M51 mail-group hooks (issue #201).
//
// Routes (panel-api/internal/api/mailgroups.go):
//   - GET    /domains/:id/mailgroups        → useMailGroups
//   - POST   /domains/:id/mailgroups        → useCreateMailGroup
//   - GET    /admin/mailgroups              → useAdminMailGroups
//   - GET    /mailgroups/:gid               → useMailGroup (detail + members)
//   - PATCH  /mailgroups/:gid               → useUpdateMailGroup
//   - PUT    /mailgroups/:gid/members       → useSetMailGroupMembers
//   - DELETE /mailgroups/:gid               → useDeleteMailGroup
//
// Wire-contract note (per feedback_verify_wire_contract): list endpoints
// return {data, total} (verified against the handler); detail returns the
// group object inline with a `members` array.
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export interface MailGroup {
  id: string;
  domain_id: string;
  local_part: string;
  email: string;
  display_name: string;
  description: string;
  group_kind: string;
  has_mailbox: boolean;
  has_calendar: boolean;
  has_addressbook: boolean;
  has_files: boolean;
  internal_only: boolean;
  member_count: number;
  created_at: string;
  updated_at: string;
}

export interface MailGroupMember {
  mailbox_id: string;
  email: string;
}

export interface MailGroupDetail extends MailGroup {
  members: MailGroupMember[];
}

export interface AdminMailGroup extends MailGroup {
  domain_name: string;
  user_username: string;
}

export interface CreateMailGroupInput {
  name: string;
  display_name?: string;
  description?: string;
  group_kind?: string;
  has_mailbox?: boolean;
  has_calendar?: boolean;
  has_addressbook?: boolean;
  has_files?: boolean;
  internal_only?: boolean;
}

export interface UpdateMailGroupInput {
  display_name?: string;
  description?: string;
  has_mailbox?: boolean;
  has_calendar?: boolean;
  has_addressbook?: boolean;
  has_files?: boolean;
  internal_only?: boolean;
}

export function useMailGroups(domainId: string | undefined): UseQueryResult<MailGroup[]> {
  return useQuery({
    queryKey: ["list", "mailgroups", domainId],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: MailGroup[] }>(
        `/domains/${domainId}/mailgroups`,
      );
      return data.data ?? [];
    },
    enabled: !!domainId,
  });
}

export function useAdminMailGroups(): UseQueryResult<AdminMailGroup[]> {
  return useQuery({
    queryKey: ["admin", "mailgroups"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: AdminMailGroup[] }>("/admin/mailgroups");
      return data.data ?? [];
    },
  });
}

export function useMailGroup(groupId: string | undefined): UseQueryResult<MailGroupDetail> {
  return useQuery({
    queryKey: ["one", "mailgroup", groupId],
    queryFn: async () => {
      const { data } = await apiClient.get<MailGroupDetail>(`/mailgroups/${groupId}`);
      return data;
    },
    enabled: !!groupId,
  });
}

function invalidateGroups(qc: ReturnType<typeof useQueryClient>, domainId?: string) {
  qc.invalidateQueries({ queryKey: ["list", "mailgroups", domainId] });
  qc.invalidateQueries({ queryKey: ["admin", "mailgroups"] });
}

function invalidateMemberships(qc: ReturnType<typeof useQueryClient>, domainId?: string) {
  qc.invalidateQueries({ queryKey: ["list", "mailbox-group-memberships", domainId] });
}

export function useCreateMailGroup(): UseMutationResult<
  MailGroup,
  unknown,
  { domainId: string; input: CreateMailGroupInput }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ domainId, input }) => {
      const { data } = await apiClient.post<MailGroup>(`/domains/${domainId}/mailgroups`, input);
      return data;
    },
    onSuccess: (_d, { domainId }) => invalidateGroups(qc, domainId),
  });
}

export function useUpdateMailGroup(): UseMutationResult<
  MailGroup,
  unknown,
  { id: string; domainId?: string; input: UpdateMailGroupInput }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, input }) => {
      const { data } = await apiClient.patch<MailGroup>(`/mailgroups/${id}`, input);
      return data;
    },
    onSuccess: (_d, { id, domainId }) => {
      invalidateGroups(qc, domainId);
      qc.invalidateQueries({ queryKey: ["one", "mailgroup", id] });
    },
  });
}

export function useSetMailGroupMembers(): UseMutationResult<
  { data: { member_count: number } },
  unknown,
  { id: string; domainId?: string; mailbox_ids: string[] }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, mailbox_ids }) => {
      const { data } = await apiClient.put<{ data: { member_count: number } }>(
        `/mailgroups/${id}/members`,
        { mailbox_ids },
      );
      return data;
    },
    onSuccess: (_d, { id, domainId }) => {
      invalidateGroups(qc, domainId);
      // JAB-372: replacing the whole member set changes the mailbox→group edge
      // set, so the mailbox table's group badges (mailbox-group-memberships
      // projection) must refresh too — not just the group detail.
      invalidateMemberships(qc, domainId);
      qc.invalidateQueries({ queryKey: ["one", "mailgroup", id] });
    },
  });
}

// useAddMailboxToGroup adds a single mailbox to a group without touching
// existing members — used by the create-mailbox wizard's "add to groups"
// step (POST /mailgroups/:gid/members/:mbid).
export function useAddMailboxToGroup(): UseMutationResult<
  void,
  unknown,
  { groupId: string; mailboxId: string; domainId?: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ groupId, mailboxId }) => {
      await apiClient.post(`/mailgroups/${groupId}/members/${mailboxId}`);
    },
    onSuccess: (_d, { groupId, domainId }) => {
      invalidateGroups(qc, domainId);
      invalidateMemberships(qc, domainId);
      qc.invalidateQueries({ queryKey: ["one", "mailgroup", groupId] });
    },
  });
}

// useRemoveMailboxFromGroup drops a single mailbox from a group (GH #238 —
// managed from the edit-mailbox modal). DELETE /mailgroups/:gid/members/:mbid.
export function useRemoveMailboxFromGroup(): UseMutationResult<
  void,
  unknown,
  { groupId: string; mailboxId: string; domainId?: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ groupId, mailboxId }) => {
      await apiClient.delete(`/mailgroups/${groupId}/members/${mailboxId}`);
    },
    onSuccess: (_d, { groupId, domainId }) => {
      invalidateGroups(qc, domainId);
      invalidateMemberships(qc, domainId);
      qc.invalidateQueries({ queryKey: ["one", "mailgroup", groupId] });
    },
  });
}

export function useDeleteMailGroup(): UseMutationResult<
  void,
  unknown,
  { id: string; domainId?: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id }) => {
      await apiClient.delete(`/mailgroups/${id}`);
    },
    // JAB-372: deleting a group drops every mailbox's membership in it, so the
    // mailbox table's group badges must refresh, not only the group list.
    onSuccess: (_d, { domainId }) => {
      invalidateGroups(qc, domainId);
      invalidateMemberships(qc, domainId);
    },
  });
}

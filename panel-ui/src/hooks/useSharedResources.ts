// useSharedResources.ts — M52 (ADR-0133) shared resources (standalone shared
// calendars / address books / file folders) + their grants. DB is truth; the
// panel reconciler converges to Stalwart shareWith.
import { useMutation, useQuery, useQueryClient, type UseMutationResult } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export type SharedResourceKind = "mailbox" | "calendar" | "addressbook" | "files";
// "readdelete" (read and delete, nothing else) is for Files resources only —
// GH #2503 scans folders.
export type GrantLevel = "read" | "readwrite" | "admin" | "readdelete";
export type GranteeKind = "mailbox" | "group";

export interface SharedResource {
  id: string;
  domain_id: string;
  kind: SharedResourceKind;
  email?: string | null;
  display_name: string;
  host_account_id: string;
  collection_id: string;
  created_at: string;
  updated_at: string;
}

export interface SharedResourceGrant {
  resource_id: string;
  grantee_kind: GranteeKind;
  grantee_id: string;
  rights: GrantLevel;
}

export interface SharedResourceDetail extends SharedResource {
  grants: SharedResourceGrant[];
}

export interface CreateSharedResourceInput {
  domainId: string;
  name: string;
  kind: SharedResourceKind;
  display_name: string;
}

const KEY = "shared_resources";

export function useCreateSharedResource(): UseMutationResult<
  SharedResource,
  unknown,
  CreateSharedResourceInput
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ domainId, name, kind, display_name }) => {
      const { data } = await apiClient.post<SharedResource>(
        `/domains/${domainId}/shared-resources`,
        { name, kind, display_name },
      );
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: [KEY] }),
  });
}

export function useDeleteSharedResource(): UseMutationResult<void, unknown, { id: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id }) => {
      await apiClient.delete(`/shared-resources/${id}`);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: [KEY] }),
  });
}

export async function fetchSharedResourceDetail(id: string): Promise<SharedResourceDetail> {
  const { data } = await apiClient.get<SharedResourceDetail>(`/shared-resources/${id}`);
  return data;
}

export function useSetSharedResourceGrants(): UseMutationResult<
  void,
  unknown,
  { id: string; grants: Omit<SharedResourceGrant, "resource_id">[] }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, grants }) => {
      await apiClient.put(`/shared-resources/${id}/grants`, { grants });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: [KEY] }),
  });
}

// GH #2503: a Files resource's scanner — an FTP/SFTP login homed at a drop
// folder whose finished files the panel moves into the shared folder.
export interface ScannerState {
  enabled: boolean;
  username?: string;
  drop_path?: string;
  max_mb?: number;
  ingested_total: number;
  last_ingest_at?: string;
  last_error?: string;
  isolated: boolean;
  ftp_access: boolean;
  sftp_access: boolean;
  login_missing?: boolean;
}

export interface EnableScannerInput {
  id: string;
  label: string;
  password: string;
  max_mb: number;
}

const scannerKey = (id: string) => ["shared_resource_scanner", id];

export function useScanner(id: string) {
  return useQuery({
    queryKey: scannerKey(id),
    queryFn: async () => {
      const { data } = await apiClient.get<ScannerState>(`/shared-resources/${id}/scanner`);
      return data;
    },
    // The ingest loop updates the counts every 30s while the drawer is open.
    refetchInterval: 30_000,
  });
}

export function useEnableScanner(): UseMutationResult<ScannerState, unknown, EnableScannerInput> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, label, password, max_mb }) => {
      const { data } = await apiClient.put<ScannerState>(`/shared-resources/${id}/scanner`, {
        label,
        password,
        max_mb,
      });
      return data;
    },
    onSuccess: (data, { id }) => qc.setQueryData(scannerKey(id), data),
  });
}

export function useDisableScanner(): UseMutationResult<void, unknown, { id: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id }) => {
      await apiClient.delete(`/shared-resources/${id}/scanner`);
    },
    onSuccess: (_d, { id }) => qc.invalidateQueries({ queryKey: scannerKey(id) }),
  });
}

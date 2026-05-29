// Hooks for M50 per-directory password protection. Mirrors
// useDomainIPACL: rule list/create/update/delete + nested credential
// list/create/delete. Cache invalidation on per-domain (rules) and
// per-rule (credentials) keys.
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export type DirectoryPrivacyRule = {
  id: string;
  domain_id: string;
  path: string;
  realm: string;
  created_at: string;
  updated_at: string;
};

export type DirectoryPrivacyCredential = {
  id: string;
  rule_id: string;
  username: string;
  created_at: string;
};

export type CreateRuleInput = {
  path: string;
  realm?: string;
};

export type UpdateRuleInput = {
  realm: string;
};

export type CreateCredentialInput = {
  username: string;
  password: string;
};

const rulesKey = (domainId: string) => [
  "list",
  "domain-directory-privacy",
  domainId,
];

const credsKey = (domainId: string, ruleId: string) => [
  "list",
  "domain-directory-privacy-credentials",
  domainId,
  ruleId,
];

// --- rules ---

export function useDirectoryPrivacyRules(
  domainId: string | undefined,
): UseQueryResult<{ data: DirectoryPrivacyRule[]; total: number }> {
  return useQuery({
    queryKey: rulesKey(domainId ?? ""),
    queryFn: async () => {
      const { data } = await apiClient.get<{
        data: DirectoryPrivacyRule[];
        total: number;
      }>(`/domains/${domainId}/directory-privacy`);
      return data;
    },
    enabled: !!domainId,
  });
}

export function useCreateDirectoryPrivacyRule(
  domainId: string,
): UseMutationResult<DirectoryPrivacyRule, unknown, CreateRuleInput> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input) => {
      const { data } = await apiClient.post<DirectoryPrivacyRule>(
        `/domains/${domainId}/directory-privacy`,
        input,
      );
      return data;
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: rulesKey(domainId) });
    },
  });
}

export function useUpdateDirectoryPrivacyRule(
  domainId: string,
): UseMutationResult<
  DirectoryPrivacyRule,
  unknown,
  { ruleId: string; input: UpdateRuleInput }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ ruleId, input }) => {
      const { data } = await apiClient.put<DirectoryPrivacyRule>(
        `/domains/${domainId}/directory-privacy/${ruleId}`,
        input,
      );
      return data;
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: rulesKey(domainId) });
    },
  });
}

export function useDeleteDirectoryPrivacyRule(
  domainId: string,
): UseMutationResult<void, unknown, { ruleId: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ ruleId }) => {
      await apiClient.delete(
        `/domains/${domainId}/directory-privacy/${ruleId}`,
      );
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: rulesKey(domainId) });
    },
  });
}

// --- credentials ---

export function useDirectoryPrivacyCredentials(
  domainId: string | undefined,
  ruleId: string | undefined,
): UseQueryResult<{ data: DirectoryPrivacyCredential[]; total: number }> {
  return useQuery({
    queryKey: credsKey(domainId ?? "", ruleId ?? ""),
    queryFn: async () => {
      const { data } = await apiClient.get<{
        data: DirectoryPrivacyCredential[];
        total: number;
      }>(`/domains/${domainId}/directory-privacy/${ruleId}/credentials`);
      return data;
    },
    enabled: !!domainId && !!ruleId,
  });
}

export function useCreateDirectoryPrivacyCredential(
  domainId: string,
  ruleId: string,
): UseMutationResult<
  DirectoryPrivacyCredential,
  unknown,
  CreateCredentialInput
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input) => {
      const { data } = await apiClient.post<DirectoryPrivacyCredential>(
        `/domains/${domainId}/directory-privacy/${ruleId}/credentials`,
        input,
      );
      return data;
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: credsKey(domainId, ruleId) });
    },
  });
}

export function useDeleteDirectoryPrivacyCredential(
  domainId: string,
  ruleId: string,
): UseMutationResult<void, unknown, { credId: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ credId }) => {
      await apiClient.delete(
        `/domains/${domainId}/directory-privacy/${ruleId}/credentials/${credId}`,
      );
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: credsKey(domainId, ruleId) });
    },
  });
}

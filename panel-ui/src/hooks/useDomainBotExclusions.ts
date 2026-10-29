// Hooks for the paths of a domain that skip the bot-detection challenge
// (GH #2328, admin only). Same list/create/delete shape as useDomainIPACL.
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";

import { apiClient } from "../apiClient";

// The methods an exclusion can name; none selected means every method.
export const BOT_EXCLUSION_METHODS = [
  "GET",
  "HEAD",
  "POST",
  "PUT",
  "PATCH",
  "DELETE",
  "OPTIONS",
] as const;

export type DomainBotExclusion = {
  id: string;
  domain_id: string;
  path_prefix: string;
  methods: string[];
  note: string;
  created_at: string;
};

export type CreateBotExclusionInput = {
  path_prefix: string;
  methods?: string[];
  note?: string;
};

const listKey = (domainId: string) => ["list", "domain-bot-exclusions", domainId];

export function useDomainBotExclusions(
  domainId: string | undefined,
): UseQueryResult<{ data: DomainBotExclusion[]; total: number }> {
  return useQuery({
    queryKey: listKey(domainId ?? ""),
    queryFn: async () => {
      const { data } = await apiClient.get<{
        data: DomainBotExclusion[];
        total: number;
      }>(`/domains/${domainId}/bot-challenge-exclusions`);
      return data;
    },
    enabled: !!domainId,
  });
}

export function useCreateDomainBotExclusion(
  domainId: string,
): UseMutationResult<DomainBotExclusion, unknown, CreateBotExclusionInput> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input) => {
      const { data } = await apiClient.post<DomainBotExclusion>(
        `/domains/${domainId}/bot-challenge-exclusions`,
        input,
      );
      return data;
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: listKey(domainId) });
    },
  });
}

export function useDeleteDomainBotExclusion(
  domainId: string,
): UseMutationResult<void, unknown, { id: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id }) => {
      await apiClient.delete(`/domains/${domainId}/bot-challenge-exclusions/${id}`);
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: listKey(domainId) });
    },
  });
}

// useQueries.ts — thin TanStack Query hooks shaped for panel-api's REST
// conventions. Purpose: a drop-in replacement for Refine's useList /
// useOne / useCreate / useUpdate / useDelete without the Refine
// provider chain.
//
// Conventions (match panel-api — see panel-api/internal/api/users.go
// for the envelope definition):
//   - List:   GET    /api/v1/{resource}?page=&page_size=&q=&sort=&order=
//             → { data: T[], total, page, page_size }
//   - One:    GET    /api/v1/{resource}/{id}  → T
//   - Create: POST   /api/v1/{resource}        → T
//   - Update: PATCH  /api/v1/{resource}/{id}   → T
//   - Delete: DELETE /api/v1/{resource}/{id}   → 204
//
// The wire envelope uses `data` + `page_size`, but consumers see the
// more React-idiomatic `items` + `pageSize` — the hook translates.
// Callers pass `pageSize` in, we serialize as `page_size` on the wire
// and unwrap `data` back to `items` on the way out.
//
// Cache keys are stable tuples so we can invalidate surgically:
//   ["list", resource, params] — list-scoped, params-aware
//   ["one",  resource, id]     — single-record
//
// On create/update/delete we invalidate the list root (["list", resource])
// which matches ALL paginated/filtered variants. Update additionally
// invalidates ["one", resource, id].
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";

import { apiClient } from "../apiClient";

// ---------------------------------------------------------------------------
// List query
// ---------------------------------------------------------------------------

export type ListParams = {
  page?: number;
  pageSize?: number;
  q?: string;
  sort?: string;
  order?: "asc" | "desc";
  // Extra filter keys panel-api accepts (e.g. is_admin=true on /users).
  // Unknown keys are forwarded as query params as-is.
  [key: string]: string | number | boolean | undefined;
};

export type ListResponse<T> = {
  items: T[];
  total: number;
};

// Raw wire envelope — panel-api returns `data` (not `items`) plus the
// echo fields we don't re-expose. Declared here so the hook can type
// the unwrap without leaking the wire shape into the public API.
type RawListEnvelope<T> = {
  data?: T[];
  items?: T[];
  total?: number;
  page?: number;
  page_size?: number;
};

export type UseListQueryResult<T> = UseQueryResult<ListResponse<T>> & {
  items: T[];
  total: number;
};

function toQueryString(params: ListParams): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue;
    // panel-api expects `page_size` (snake_case) on the wire. Keep
    // callers using the camelCase `pageSize` field to stay idiomatic
    // on the JS side and translate at the boundary.
    const wireKey = k === "pageSize" ? "page_size" : k;
    sp.set(wireKey, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}

export function useListQuery<T>({
  resource,
  params = {},
  enabled = true,
}: {
  resource: string;
  params?: ListParams;
  enabled?: boolean;
}): UseListQueryResult<T> {
  const q = useQuery({
    queryKey: ["list", resource, params],
    queryFn: async () => {
      const { data: raw } = await apiClient.get<RawListEnvelope<T>>(
        `/${resource}${toQueryString(params)}`,
      );
      return {
        items: raw.data ?? raw.items ?? [],
        total: raw.total ?? 0,
      };
    },
    enabled,
  });
  return {
    ...q,
    items: q.data?.items ?? [],
    total: q.data?.total ?? 0,
  };
}

// ---------------------------------------------------------------------------
// One query
// ---------------------------------------------------------------------------

export function useOneQuery<T>({
  resource,
  id,
  enabled = true,
}: {
  resource: string;
  id: string | undefined;
  enabled?: boolean;
}): UseQueryResult<T> {
  return useQuery({
    queryKey: ["one", resource, id],
    queryFn: async () => {
      const { data } = await apiClient.get<T>(`/${resource}/${id}`);
      return data;
    },
    enabled: enabled && !!id,
  });
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

export function useCreateMutation<T, Input = Partial<T>>({
  resource,
}: {
  resource: string;
}): UseMutationResult<T, unknown, Input> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: Input) => {
      const { data } = await apiClient.post<T>(`/${resource}`, input);
      return data;
    },
    // Awaiting invalidateQueries inside onSuccess makes mutateAsync()
    // resolve only after the list refetch is in flight + completed,
    // so callers that close a Drawer right after `await mutateAsync(...)`
    // observe the new row before the drawer is gone. Without the await
    // the create returns 201, drawer closes, and the list briefly
    // shows the previous (stale) page until React re-renders post-
    // refetch — a race that surfaces as "silent create failure" in
    // E2E tests that assert too quickly.
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["list", resource] });
    },
  });
}

export function useUpdateMutation<T, Input = Partial<T>>({
  resource,
}: {
  resource: string;
}): UseMutationResult<T, unknown, { id: string; input: Input }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, input }) => {
      const { data } = await apiClient.patch<T>(`/${resource}/${id}`, input);
      return data;
    },
    onSuccess: async (_data, { id }) => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["list", resource] }),
        qc.invalidateQueries({ queryKey: ["one", resource, id] }),
      ]);
    },
  });
}

export function useDeleteMutation({
  resource,
}: {
  resource: string;
}): UseMutationResult<void, unknown, { id: string; query?: Record<string, string> }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, query }) => {
      // Optional query string (e.g. domains: ?delete_files=true, GH #1382).
      const qs = query ? `?${new URLSearchParams(query).toString()}` : "";
      await apiClient.delete(`/${resource}/${id}${qs}`);
    },
    onSuccess: async (_data, { id }) => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["list", resource] }),
        qc.invalidateQueries({ queryKey: ["one", resource, id] }),
      ]);
    },
  });
}

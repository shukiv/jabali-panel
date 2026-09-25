// JAB-370 AC7: mailbox, group-membership, autoresponder, and forwarder
// mutations invalidate one canonical inventory query family. These tests seed a
// QueryClient with the keys the inventory views actually render from and check
// which of them a mutation marks invalid — prefix semantics included, so a key
// that is invalidated by exact match only would fail here.
import { QueryClient, QueryClientProvider, type QueryKey } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useDeleteMailbox } from "./useMailboxes";

vi.mock("../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as { delete: ReturnType<typeof vi.fn> };

// Every view that renders a mailbox, or a summary keyed off one.
const INVENTORY_KEYS: QueryKey[] = [
  ["list", "me/mailboxes", { page: 1, pageSize: 20 }], // tenant Workspace
  ["list", "me/mailboxes", { recent: 5 }], // dashboard Recent card
  ["list", "admin/mailboxes", { page: 1 }], // admin Directory
  ["list", "domains/dom1/mailboxes", { page: 1 }], // per-domain drill-down
  ["list", "mailboxes", "dom1", { page: 1 }], // legacy per-domain hook
  ["list", "mailbox-group-memberships", "me"], // membership summaries
  ["autoresponders", "by-domain", "me"], // autoresponder summaries
  ["forwarders"], // forwarder summaries
];

// Keys outside the family that a mailbox mutation must leave alone.
const UNRELATED_KEYS: QueryKey[] = [
  ["list", "domains", { page: 1 }],
  ["list", "databases", { page: 1 }],
];

function seed(qc: QueryClient) {
  for (const k of [...INVENTORY_KEYS, ...UNRELATED_KEYS]) qc.setQueryData(k, { seeded: true });
}

function invalidated(qc: QueryClient, keys: QueryKey[]): QueryKey[] {
  return keys.filter((k) => qc.getQueryState(k)?.isInvalidated);
}

beforeEach(() => {
  mocked.delete.mockReset().mockResolvedValue({ data: undefined });
});

describe("JAB-370 AC7 — one canonical mail inventory invalidation family", () => {
  it("deleting a mailbox invalidates every inventory view, including forwarder summaries", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seed(qc);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useDeleteMailbox(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ id: "mb1", domainId: "dom1" });
    });

    // The server cascades a mailbox's forwarders and autoresponders on delete,
    // so their summaries must refresh with the mailbox lists.
    expect(invalidated(qc, INVENTORY_KEYS)).toEqual(INVENTORY_KEYS);
    expect(invalidated(qc, UNRELATED_KEYS)).toEqual([]);
  });
});

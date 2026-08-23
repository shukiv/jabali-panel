// useMailGroups.test.tsx — JAB-372: every mutation that changes the
// mailbox→group edge set must invalidate the mailbox-group-memberships
// projection (the mailbox table's group badges), or the badges go stale until
// an unrelated refetch. Guards the whole mutation→invalidation matrix.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  useAddMailboxToGroup,
  useCreateMailGroup,
  useDeleteMailGroup,
  useRemoveMailboxFromGroup,
  useSetMailGroupMembers,
  useUpdateMailGroup,
} from "./useMailGroups";

vi.mock("../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as Record<"get" | "post" | "patch" | "put" | "delete", ReturnType<typeof vi.fn>>;

function makeWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return { qc, wrapper };
}

const MEMBERSHIPS_KEY = ["list", "mailbox-group-memberships", "dom1"];

function invalidatedMemberships(calls: unknown[][]): boolean {
  return calls.some(
    (c) => JSON.stringify((c[0] as { queryKey?: unknown[] })?.queryKey) === JSON.stringify(MEMBERSHIPS_KEY),
  );
}

beforeEach(() => {
  for (const k of ["get", "post", "patch", "put", "delete"] as const) {
    mocked[k].mockReset().mockResolvedValue({ data: {} });
  }
});

describe("useMailGroups membership invalidation matrix (JAB-372)", () => {
  it("useSetMailGroupMembers (replace set) refreshes mailbox badges", async () => {
    const { qc, wrapper } = makeWrapper();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useSetMailGroupMembers(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ id: "g1", domainId: "dom1", mailbox_ids: ["m1", "m2"] });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocked.put).toHaveBeenCalledWith("/mailgroups/g1/members", { mailbox_ids: ["m1", "m2"] });
    expect(invalidatedMemberships(spy.mock.calls as unknown[][])).toBe(true);
  });

  it("useDeleteMailGroup removes badges without reload", async () => {
    const { qc, wrapper } = makeWrapper();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useDeleteMailGroup(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ id: "g1", domainId: "dom1" });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocked.delete).toHaveBeenCalledWith("/mailgroups/g1");
    expect(invalidatedMemberships(spy.mock.calls as unknown[][])).toBe(true);
  });

  it("single add still invalidates memberships", async () => {
    const { qc, wrapper } = makeWrapper();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useAddMailboxToGroup(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ groupId: "g1", mailboxId: "m1", domainId: "dom1" });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(invalidatedMemberships(spy.mock.calls as unknown[][])).toBe(true);
  });

  it("single remove still invalidates memberships", async () => {
    const { qc, wrapper } = makeWrapper();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useRemoveMailboxFromGroup(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ groupId: "g1", mailboxId: "m1", domainId: "dom1" });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(invalidatedMemberships(spy.mock.calls as unknown[][])).toBe(true);
  });

  it("create (empty group) does NOT touch the membership projection", async () => {
    const { qc, wrapper } = makeWrapper();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useCreateMailGroup(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ domainId: "dom1", input: { name: "team" } });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(invalidatedMemberships(spy.mock.calls as unknown[][])).toBe(false);
  });

  it("update (metadata only) does NOT touch the membership projection", async () => {
    const { qc, wrapper } = makeWrapper();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useUpdateMailGroup(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ id: "g1", domainId: "dom1", input: { display_name: "Team" } });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(invalidatedMemberships(spy.mock.calls as unknown[][])).toBe(false);
  });
});

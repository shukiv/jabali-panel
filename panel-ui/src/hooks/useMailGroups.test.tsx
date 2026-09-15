// useMailGroups.test.tsx — JAB-372: every mutation that changes the
// mailbox→group edge set must invalidate the mailbox-group-memberships
// projection (the mailbox table's group badges), or the badges go stale until
// an unrelated refetch. Guards the whole mutation→invalidation matrix.
//
// JAB-370 Selection: the projection is now read through useMailboxGroupMemberships
// — ONE owner-scoped bulk request (cross-domain Mailboxes tab) or the per-domain
// endpoint in the drill-down, replacing the per-domain fan-out. The invalidation
// matrix below busts the key PREFIX (no domain slot) so both the bulk key
// (["list","mailbox-group-memberships","me"]) and the drill-down key refresh.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  useAddMailboxToGroup,
  useCreateMailGroup,
  useDeleteMailGroup,
  useMailboxGroupMemberships,
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

// JAB-370 Selection: invalidation is busted at the PREFIX (no domain slot) so the
// owner-scoped bulk key ["list","mailbox-group-memberships","me"] refreshes
// alongside the per-domain drill-down key.
const MEMBERSHIPS_KEY = ["list", "mailbox-group-memberships"];

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

// JAB-370 Selection: the read side. useMailboxGroupMemberships picks the owner-
// scoped bulk endpoint for the cross-domain tab and the per-domain endpoint in
// the drill-down — one request either way, replacing the per-domain fan-out.
describe("useMailboxGroupMemberships (JAB-370 Selection)", () => {
  it("cross-domain (no domainId): ONE owner-scoped GET /mail/mailbox-group-memberships, no per-domain fan-out", async () => {
    mocked.get.mockResolvedValue({
      data: { data: { mb1: [{ group_id: "g1", group_name: "Team", group_email: "team@one.test" }] } },
    });
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxGroupMemberships(undefined, true), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // Exactly one call, to the bulk endpoint — a regression back to the
    // per-domain fan-out would issue one /domains/:id/... call per domain.
    expect(mocked.get).toHaveBeenCalledTimes(1);
    expect(mocked.get).toHaveBeenCalledWith("/mail/mailbox-group-memberships");
    expect(
      mocked.get.mock.calls.filter(([u]) => String(u).includes("/domains/")),
    ).toHaveLength(0);
    // Unwraps the {data:{...}} envelope to the mailbox->groups map.
    expect(result.current.data?.mb1).toHaveLength(1);
    expect(result.current.data?.mb1[0].group_email).toBe("team@one.test");
  });

  it("drill-down (domainId set): GETs the per-domain endpoint, never the bulk one", async () => {
    mocked.get.mockResolvedValue({ data: { data: {} } });
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxGroupMemberships("dom1", true), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocked.get).toHaveBeenCalledTimes(1);
    expect(mocked.get).toHaveBeenCalledWith("/domains/dom1/mailbox-group-memberships");
    expect(
      mocked.get.mock.calls.filter(([u]) => String(u) === "/mail/mailbox-group-memberships"),
    ).toHaveLength(0);
  });

  it("is disabled (no fetch) when enabled=false", () => {
    const { wrapper } = makeWrapper();
    renderHook(() => useMailboxGroupMemberships(undefined, false), { wrapper });
    expect(mocked.get).not.toHaveBeenCalled();
  });
});

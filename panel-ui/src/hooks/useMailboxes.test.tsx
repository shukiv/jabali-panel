// useMailboxes.test.tsx — smoke tests that the M6 mailbox hooks hit
// the right routes, unwrap the panel-api `{data,total}` envelope, and
// invalidate the right cache keys after mutations.
//
// Pure hook tests (no React tree) — the UI components use these hooks
// straight through, so a regression here breaks both admin and user
// shells at once.
import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  useCreateMailbox,
  useDeleteMailbox,
  useDisableDomainEmail,
  useDomainEmail,
  useEnableDomainEmail,
  useMailboxes,
  useRotateMailboxPassword,
} from "./useMailboxes";

vi.mock("../apiClient", () => ({
  apiClient: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
}));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function makeWrapper() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return { qc, wrapper };
}

beforeEach(() => {
  mocked.get.mockReset();
  mocked.post.mockReset();
  mocked.patch.mockReset();
  mocked.delete.mockReset();
});

describe("useDomainEmail", () => {
  it("GETs /domains/:id/email and exposes response as data", async () => {
    mocked.get.mockResolvedValue({
      data: {
        domain_id: "dom1",
        domain_name: "example.com",
        email_enabled: true,
        dkim_selector: "jabali",
        dkim_public_key: "v=DKIM1;k=ed25519;p=AAAA",
        records: [],
      },
    });

    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useDomainEmail("dom1"), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocked.get).toHaveBeenCalledWith("/domains/dom1/email");
    expect(result.current.data?.email_enabled).toBe(true);
    expect(result.current.data?.dkim_selector).toBe("jabali");
  });

  it("is disabled (no fetch) when domainId is undefined", () => {
    const { wrapper } = makeWrapper();
    renderHook(() => useDomainEmail(undefined), { wrapper });
    // No call fired because `enabled: false` keeps useQuery idle.
    expect(mocked.get).not.toHaveBeenCalled();
  });
});

describe("useEnableDomainEmail", () => {
  it("POSTs /domains/:id/email and invalidates domain-email + domains caches", async () => {
    mocked.post.mockResolvedValue({ data: { email_enabled: true } });
    const { qc, wrapper } = makeWrapper();
    const invalidate = vi.spyOn(qc, "invalidateQueries");

    const { result } = renderHook(() => useEnableDomainEmail(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ domainId: "dom1" });
    });

    expect(mocked.post).toHaveBeenCalledWith("/domains/dom1/email");
    // All four cache keys the enable flow touches must be invalidated,
    // otherwise the UI keeps showing the stale "disabled" state or
    // the mailbox-create button stays blocked until the user reloads.
    const invalidatedKeys = invalidate.mock.calls.map((c) => c[0]?.queryKey);
    expect(invalidatedKeys).toContainEqual(["one", "domain-email", "dom1"]);
    expect(invalidatedKeys).toContainEqual(["one", "domains", "dom1"]);
    expect(invalidatedKeys).toContainEqual(["list", "domains"]);
    expect(invalidatedKeys).toContainEqual(["list", "mailboxes", "dom1"]);
  });
});

describe("useDisableDomainEmail", () => {
  it("DELETEs /domains/:id/email", async () => {
    mocked.delete.mockResolvedValue({ data: undefined });
    const { wrapper } = makeWrapper();

    const { result } = renderHook(() => useDisableDomainEmail(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ domainId: "dom1" });
    });

    expect(mocked.delete).toHaveBeenCalledWith("/domains/dom1/email");
  });
});

describe("useMailboxes", () => {
  it("GETs /domains/:id/mailboxes with snake_case page_size and unwraps data→items", async () => {
    mocked.get.mockResolvedValue({
      data: {
        data: [
          {
            id: "mb1",
            domain_id: "dom1",
            email: "alice@example.com",
            quota_bytes: 1 << 30,
            is_disabled: false,
            last_usage_bytes: 0,
            created_at: "2026-04-21T00:00:00Z",
            updated_at: "2026-04-21T00:00:00Z",
          },
        ],
        total: 1,
      },
    });

    const { wrapper } = makeWrapper();
    const { result } = renderHook(
      () => useMailboxes({ domainId: "dom1", params: { page: 1, pageSize: 20 } }),
      { wrapper },
    );

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // Confirms two contract pieces at once: correct nested URL AND the
    // camelCase→snake_case translation for `pageSize`. If either
    // regresses, every mailbox page silently shows an empty list.
    expect(mocked.get).toHaveBeenCalledWith(
      "/domains/dom1/mailboxes?page=1&page_size=20",
    );
    expect(result.current.items).toHaveLength(1);
    expect(result.current.items[0].email).toBe("alice@example.com");
    expect(result.current.total).toBe(1);
  });

  it("is disabled when domainId is undefined", () => {
    const { wrapper } = makeWrapper();
    renderHook(() => useMailboxes({ domainId: undefined }), { wrapper });
    expect(mocked.get).not.toHaveBeenCalled();
  });
});

describe("useCreateMailbox", () => {
  it("POSTs /domains/:id/mailboxes and invalidates that domain's mailbox list", async () => {
    mocked.post.mockResolvedValue({
      data: { id: "mb1", email: "alice@example.com", quota_bytes: 1 << 30, password: "gen123" },
    });
    const { qc, wrapper } = makeWrapper();
    const invalidate = vi.spyOn(qc, "invalidateQueries");

    const { result } = renderHook(() => useCreateMailbox(), { wrapper });
    const resp = await act(async () =>
      result.current.mutateAsync({
        domainId: "dom1",
        input: { local_part: "alice" },
      }),
    );

    expect(mocked.post).toHaveBeenCalledWith("/domains/dom1/mailboxes", {
      local_part: "alice",
    });
    // Reveal-once password must flow through the hook untouched, or
    // the UI modal shows nothing for auto-generated passwords.
    expect(resp?.password).toBe("gen123");
    const invalidatedKeys = invalidate.mock.calls.map((c) => c[0]?.queryKey);
    expect(invalidatedKeys).toContainEqual(["list", "mailboxes", "dom1"]);
  });
});

describe("useDeleteMailbox", () => {
  it("DELETEs /mailboxes/:id and invalidates the correct domain's list", async () => {
    mocked.delete.mockResolvedValue({ data: undefined });
    const { qc, wrapper } = makeWrapper();
    const invalidate = vi.spyOn(qc, "invalidateQueries");

    const { result } = renderHook(() => useDeleteMailbox(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ id: "mb1", domainId: "dom1" });
    });

    expect(mocked.delete).toHaveBeenCalledWith("/mailboxes/mb1");
    const invalidatedKeys = invalidate.mock.calls.map((c) => c[0]?.queryKey);
    expect(invalidatedKeys).toContainEqual(["list", "mailboxes", "dom1"]);
    expect(invalidatedKeys).toContainEqual(["admin", "mailboxes"]);
    // JAB-333: a deleted mailbox also leaves the tenant screen's group-membership
    // and autoresponder panels — those shared keys must be invalidated too, or
    // they render a ghost row for the deleted mailbox until the next refetch.
    expect(invalidatedKeys).toContainEqual([
      "list",
      "mailbox-group-memberships",
      "dom1",
    ]);
    expect(invalidatedKeys).toContainEqual(["autoresponders", "by-domain", "dom1"]);
  });
});

describe("useRotateMailboxPassword", () => {
  it("POSTs /mailboxes/:id/rotate-password with empty body when no new password supplied", async () => {
    mocked.post.mockResolvedValue({ data: { password: "newly-generated" } });
    const { wrapper } = makeWrapper();

    const { result } = renderHook(() => useRotateMailboxPassword(), { wrapper });
    const resp = await act(async () => result.current.mutateAsync({ id: "mb1" }));

    expect(mocked.post).toHaveBeenCalledWith(
      "/mailboxes/mb1/rotate-password",
      { new_password: "" },
    );
    expect(resp?.password).toBe("newly-generated");
  });
});

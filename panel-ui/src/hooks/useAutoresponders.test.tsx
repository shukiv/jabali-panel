// useAutoresponders.test.tsx — JAB-370 Selection: the Mailboxes tab's
// "Auto replies" column reads mailbox autoresponders through
// useMailboxAutoresponders — ONE owner-scoped bulk request (cross-domain tab)
// or the per-domain endpoint in the Mail Domains drill-down, replacing the
// one-request-per-email-enabled-domain fan-out that merged the maps in the
// browser. These guard that endpoint selection and the disabled state.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useMailboxAutoresponders } from "./useAutoresponders";

vi.mock("../apiClient", () => ({
  apiClient: { get: vi.fn() },
}));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as Record<"get", ReturnType<typeof vi.fn>>;

function makeWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return { wrapper };
}

beforeEach(() => {
  mocked.get.mockReset().mockResolvedValue({ data: { data: {} } });
});

describe("useMailboxAutoresponders (JAB-370 Selection)", () => {
  it("cross-domain (no domainId): ONE owner-scoped GET /mail/autoresponders, no per-domain fan-out", async () => {
    mocked.get.mockResolvedValue({
      data: { data: { mb1: { mailbox_id: "mb1", enabled: true, updated_at: "2026-01-01T00:00:00Z" } } },
    });
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxAutoresponders(undefined, true), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // Exactly one call, to the bulk endpoint — a regression back to the
    // per-domain fan-out would issue one /domains/:id/... call per domain.
    expect(mocked.get).toHaveBeenCalledTimes(1);
    expect(mocked.get).toHaveBeenCalledWith("/mail/autoresponders");
    expect(
      mocked.get.mock.calls.filter(([u]) => String(u).includes("/domains/")),
    ).toHaveLength(0);
    // Unwraps the {data:{...}} envelope to the mailbox->autoresponder map.
    expect(result.current.data?.mb1.enabled).toBe(true);
  });

  it("drill-down (domainId set): GETs the per-domain endpoint, never the bulk one", async () => {
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxAutoresponders("dom1", true), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocked.get).toHaveBeenCalledTimes(1);
    expect(mocked.get).toHaveBeenCalledWith("/domains/dom1/autoresponders");
    expect(
      mocked.get.mock.calls.filter(([u]) => String(u) === "/mail/autoresponders"),
    ).toHaveLength(0);
  });

  it("is disabled (no fetch) when enabled=false", () => {
    const { wrapper } = makeWrapper();
    renderHook(() => useMailboxAutoresponders(undefined, false), { wrapper });
    expect(mocked.get).not.toHaveBeenCalled();
  });
});

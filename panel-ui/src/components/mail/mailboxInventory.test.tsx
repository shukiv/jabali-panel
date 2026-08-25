// mailboxInventory.test.tsx — the Mailbox Inventory module's invariants
// (JAB-333 / JAB-354). All three mailbox inventories (admin, tenant,
// per-domain) route their webmail launch through useMailboxWebmail, so the
// opener-severing security invariant lives in exactly one tested place.

import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { post: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { warning: vi.fn(), error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";
import { formatMailboxBytes, useMailboxWebmail } from "./mailboxInventory";

const post = apiClient.post as unknown as ReturnType<typeof vi.fn>;
const warn = feedback.message.warning as unknown as ReturnType<typeof vi.fn>;
const err = feedback.message.error as unknown as ReturnType<typeof vi.fn>;

function makeWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return { wrapper };
}

// A fake popup whose opener starts non-null so we can prove it gets severed.
function fakePopup() {
  return { opener: {} as unknown, location: { href: "" }, close: vi.fn() };
}

beforeEach(() => {
  post.mockReset();
  warn.mockReset();
  err.mockReset();
});

describe("useMailboxWebmail", () => {
  it("severs popup.opener SYNCHRONOUSLY, before the async mint resolves", async () => {
    const popup = fakePopup();
    const openSpy = vi.spyOn(window, "open").mockReturnValue(popup as unknown as Window);
    // Mint that never settles during the synchronous window we assert on.
    post.mockReturnValue(new Promise(() => {}));

    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxWebmail(), { wrapper });

    act(() => result.current.launch("mb-1"));

    // The opener MUST already be null right after launch returns — i.e. before
    // any mint response could navigate the tab (reverse-tabnab guard, JAB-330).
    expect(popup.opener).toBeNull();
    openSpy.mockRestore();
  });

  it("navigates the popup to the minted URL on success", async () => {
    const popup = fakePopup();
    const openSpy = vi.spyOn(window, "open").mockReturnValue(popup as unknown as Window);
    post.mockResolvedValue({ data: { url: "https://webmail.example/sso?t=abc" } });

    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxWebmail(), { wrapper });
    act(() => result.current.launch("mb-1"));

    await waitFor(() =>
      expect(popup.location.href).toBe("https://webmail.example/sso?t=abc"),
    );
    expect(post).toHaveBeenCalledWith("/mailboxes/mb-1/sso");
    openSpy.mockRestore();
  });

  it("does NOT mint when the popup is blocked (would burn a one-shot URL)", () => {
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);

    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxWebmail(), { wrapper });
    act(() => result.current.launch("mb-1"));

    expect(post).not.toHaveBeenCalled();
    expect(warn).toHaveBeenCalledTimes(1);
    openSpy.mockRestore();
  });

  it("surfaces the typed rotate-password hint and closes the popup on mint failure", async () => {
    const popup = fakePopup();
    const openSpy = vi.spyOn(window, "open").mockReturnValue(popup as unknown as Window);
    post.mockRejectedValue({
      response: {
        data: {
          error: "sso_unavailable_rotate_password",
          detail: "Rotate the password to enable SSO.",
        },
      },
    });

    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxWebmail(), { wrapper });
    act(() => result.current.launch("mb-1"));

    await waitFor(() => expect(popup.close).toHaveBeenCalled());
    expect(warn).toHaveBeenCalledWith("Rotate the password to enable SSO.");
    expect(err).not.toHaveBeenCalled();
    openSpy.mockRestore();
  });

  it("surfaces detail on a generic mint failure", async () => {
    const popup = fakePopup();
    const openSpy = vi.spyOn(window, "open").mockReturnValue(popup as unknown as Window);
    post.mockRejectedValue({ response: { data: { detail: "boom" } } });

    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxWebmail(), { wrapper });
    act(() => result.current.launch("mb-1"));

    await waitFor(() => expect(err).toHaveBeenCalledWith("boom"));
    openSpy.mockRestore();
  });
});

describe("formatMailboxBytes", () => {
  it("uses KiB/MiB and drops the decimal for values >= 10 (the unified format)", () => {
    expect(formatMailboxBytes(0)).toBe("0 B");
    expect(formatMailboxBytes(null)).toBe("0 B");
    expect(formatMailboxBytes(512)).toBe("512 B");
    expect(formatMailboxBytes(500 * 1024 * 1024)).toBe("500 MiB");
    expect(formatMailboxBytes(1.5 * 1024 * 1024 * 1024)).toBe("1.5 GiB");
  });
});

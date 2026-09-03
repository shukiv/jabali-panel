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
import {
  formatMailboxBytes,
  useMailboxPasswordReset,
  useMailboxWebmail,
} from "./mailboxInventory";

const post = apiClient.post as unknown as ReturnType<typeof vi.fn>;
const warn = feedback.message.warning as unknown as ReturnType<typeof vi.fn>;
const err = feedback.message.error as unknown as ReturnType<typeof vi.fn>;
const success = feedback.message.success as unknown as ReturnType<typeof vi.fn>;

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

describe("useMailboxPasswordReset", () => {
  beforeEach(() => success.mockReset());

  it("reveals a SERVER-GENERATED password exactly once (no toast)", async () => {
    // No custom password → server returns one to reveal.
    post.mockResolvedValue({ data: { password: "GEN-pw-123" } });
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxPasswordReset(), { wrapper });

    let ok: boolean | undefined;
    await act(async () => {
      ok = await result.current.rotate({ id: "mb-1", email: "a@ex.com", title: "T" });
    });

    expect(ok).toBe(true);
    expect(result.current.reveal).toEqual({ email: "a@ex.com", password: "GEN-pw-123", title: "T" });
    // A revealed one-shot password must NOT also fire a toast (it would imply
    // the caller can ignore the modal).
    expect(success).not.toHaveBeenCalled();
    expect(post).toHaveBeenCalledWith("/mailboxes/mb-1/rotate-password", { new_password: "" });
  });

  it("does NOT reveal when a custom password was accepted (toast only)", async () => {
    post.mockResolvedValue({ data: {} }); // custom pw → nothing to reveal
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxPasswordReset(), { wrapper });

    await act(async () => {
      await result.current.rotate({ id: "mb-1", email: "a@ex.com", newPassword: "hunter2" });
    });

    expect(result.current.reveal).toBeNull();
    expect(success).toHaveBeenCalledTimes(1);
    expect(post).toHaveBeenCalledWith("/mailboxes/mb-1/rotate-password", { new_password: "hunter2" });
  });

  it("surfaces the server error and returns false on failure (no reveal)", async () => {
    post.mockRejectedValue({ response: { data: { error: "rate limited" } } });
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxPasswordReset(), { wrapper });

    let ok: boolean | undefined;
    await act(async () => {
      ok = await result.current.rotate({ id: "mb-1", email: "a@ex.com" });
    });

    expect(ok).toBe(false);
    expect(result.current.reveal).toBeNull();
    expect(err).toHaveBeenCalledWith("rate limited");
  });

  it("revealPassword feeds another action (create) into the same modal", async () => {
    const { wrapper } = makeWrapper();
    const { result } = renderHook(() => useMailboxPasswordReset(), { wrapper });

    act(() => result.current.revealPassword({ email: "new@ex.com", password: "P", title: "New" }));
    expect(result.current.reveal).toEqual({ email: "new@ex.com", password: "P", title: "New" });

    act(() => result.current.clearReveal());
    expect(result.current.reveal).toBeNull();
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

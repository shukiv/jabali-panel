// SupportChatBanner.test.tsx — the dashboard tells admins about the "Ask
// Jabali" support chat until one of them closes the banner (server-wide) or
// turns the chat on.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { success: toast.success, error: toast.error, info: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../apiClient";
import { SupportChatBanner } from "./SupportChatBanner";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

const TITLE = 'New: "Ask Jabali" AI support chat';

const renderBanner = (settings: object) => {
  mockGet.mockResolvedValue({ data: settings });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <SupportChatBanner />
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

const off = { support_chat_admin: false, support_chat_clients: false, support_chat_notice_dismissed: false };

describe("SupportChatBanner", () => {
  beforeEach(() => vi.clearAllMocks());

  it("tells admins about the chat while it is off, with a way to the settings", async () => {
    renderBanner(off);
    expect(await screen.findByText(TITLE)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open settings" })).toHaveAttribute("href", "/jabali-admin/settings");
  });

  it("closing it saves the dismissal for every admin", async () => {
    mockPatch.mockResolvedValue({ data: {} });
    renderBanner(off);
    await screen.findByText(TITLE);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(mockPatch).toHaveBeenCalledWith("/admin/settings", { support_chat_notice_dismissed: true }));
    await waitFor(() => expect(screen.queryByText(TITLE)).toBeNull());
  });

  it.each([
    ["dismissed", { ...off, support_chat_notice_dismissed: true }],
    ["on for the admin panel", { ...off, support_chat_admin: true }],
    ["on for the client panels", { ...off, support_chat_clients: true }],
  ])("stays hidden when %s", async (_name, settings) => {
    renderBanner(settings);
    await waitFor(() => expect(mockGet).toHaveBeenCalledWith("/admin/settings"));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText(TITLE)).toBeNull();
  });

  it("stays hidden when the settings can't be read", async () => {
    mockGet.mockRejectedValue(new Error("500"));
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <SupportChatBanner />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(mockGet).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText(TITLE)).toBeNull();
  });
});

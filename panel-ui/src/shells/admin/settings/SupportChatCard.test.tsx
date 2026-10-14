// SupportChatCard.test.tsx — Server Settings -> General: one switch per panel
// for the "Ask Jabali" support chat, each saved on its own.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: toast.success, error: toast.error, info: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { SupportChatCard } from "./SupportChatCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

const renderCard = (settings: object) => {
  mockGet.mockResolvedValue({ data: settings });
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <SupportChatCard />
    </QueryClientProvider>,
  );
};

describe("SupportChatCard", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows each panel's stored switch and says where the chat's data goes", async () => {
    renderCard({ support_chat_admin: true, support_chat_clients: false });
    expect(await screen.findByRole("switch", { name: "Admin panel" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "Client panels" })).not.toBeChecked();
    expect(screen.getByText(/widget from support\.jabali-panel\.com/)).toBeInTheDocument();
  });

  it("saves only the switch that was flipped", async () => {
    mockPatch.mockResolvedValue({ data: {} });
    renderCard({ support_chat_admin: false, support_chat_clients: false });
    fireEvent.click(await screen.findByRole("switch", { name: "Client panels" }));
    await waitFor(() => expect(mockPatch).toHaveBeenCalledWith("/admin/settings", { support_chat_clients: true }));
    await waitFor(() => expect(screen.getByRole("switch", { name: "Client panels" })).toBeChecked());
    expect(screen.getByRole("switch", { name: "Admin panel" })).not.toBeChecked();
    expect(toast.success).toHaveBeenCalledWith("Support chat turned on. It shows from the next page load.");
  });

  it("keeps the switch when the save fails", async () => {
    mockPatch.mockRejectedValue(new Error("500"));
    renderCard({ support_chat_admin: false, support_chat_clients: false });
    fireEvent.click(await screen.findByRole("switch", { name: "Admin panel" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(screen.getByRole("switch", { name: "Admin panel" })).not.toBeChecked();
  });
});

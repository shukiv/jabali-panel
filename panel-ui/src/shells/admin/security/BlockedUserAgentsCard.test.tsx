// BlockedUserAgentsCard.test.tsx — the server-wide blocked user-agent list:
// loads the saved list, refuses entries the server would refuse before
// sending them, and PATCHes the text as typed. Only apiClient is mocked.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { BlockedUserAgentsCard } from "./BlockedUserAgentsCard";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <BlockedUserAgentsCard />
    </QueryClientProvider>,
  );
}

const box = () => screen.getByRole("textbox", { name: "Blocked user agents" }) as HTMLTextAreaElement;
const saveButton = () => screen.getByRole("button", { name: "Save" });

beforeEach(() => {
  mocked.get.mockReset();
  mocked.patch.mockReset();
});

describe("BlockedUserAgentsCard", () => {
  it("shows the saved list and its count", async () => {
    mocked.get.mockResolvedValue({ data: { blocked_user_agents: "AhrefsBot\nSemrushBot" } });
    renderCard();
    await waitFor(() => expect(box().value).toBe("AhrefsBot\nSemrushBot"));
    expect(screen.getByText(/2 of 200/)).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
  });

  it("PATCHes the list to /admin/settings", async () => {
    mocked.get.mockResolvedValue({ data: { blocked_user_agents: "" } });
    mocked.patch.mockResolvedValue({ data: { blocked_user_agents: "AhrefsBot" } });
    renderCard();
    await waitFor(() => expect(box()).toBeInTheDocument());
    fireEvent.change(box(), { target: { value: "AhrefsBot\n" } });
    fireEvent.click(saveButton());
    await waitFor(() =>
      expect(mocked.patch).toHaveBeenCalledWith("/admin/settings", { blocked_user_agents: "AhrefsBot\n" }),
    );
    await waitFor(() => expect(box().value).toBe("AhrefsBot"));
  });

  it("refuses an entry nginx config must never see, without sending it", async () => {
    mocked.get.mockResolvedValue({ data: { blocked_user_agents: "" } });
    renderCard();
    await waitFor(() => expect(box()).toBeInTheDocument());
    fireEvent.change(box(), { target: { value: 'evil"; return 200;' } });
    expect(screen.getByText("Some entries cannot be used")).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
    expect(mocked.patch).not.toHaveBeenCalled();
  });

  it("says it stops only clients that name themselves", async () => {
    mocked.get.mockResolvedValue({ data: {} });
    renderCard();
    expect(await screen.findByText(/stops only clients that name/)).toBeInTheDocument();
  });
});

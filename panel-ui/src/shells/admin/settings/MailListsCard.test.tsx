// MailListsCard.test.tsx — GH #1833: Server Settings → Email caps how many
// outside addresses one distribution list may hold.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: toast.success, error: toast.error, info: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { MailListsCard } from "./MailListsCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

const setCap = (value: string) => {
  const input = screen.getByLabelText("Outside addresses per list");
  fireEvent.change(input, { target: { value } });
  fireEvent.blur(input);
};

describe("MailListsCard", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows the stored cap", async () => {
    mockGet.mockResolvedValue({ data: { mail_list_external_member_cap: 25 } });
    render(<MailListsCard />);
    expect(await screen.findByLabelText("Outside addresses per list")).toHaveValue("25");
  });

  it("saves a new cap, 0 included", async () => {
    mockGet.mockResolvedValue({ data: { mail_list_external_member_cap: 25 } });
    mockPatch.mockResolvedValue({ data: {} });
    render(<MailListsCard />);
    await screen.findByLabelText("Outside addresses per list");

    setCap("0");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mockPatch).toHaveBeenCalledWith("/admin/settings", { mail_list_external_member_cap: 0 }),
    );
    expect(toast.success).toHaveBeenCalledWith("Distribution lists can no longer take outside addresses");
  });

  it("keeps Save off until the cap changes", async () => {
    mockGet.mockResolvedValue({ data: { mail_list_external_member_cap: 25 } });
    render(<MailListsCard />);
    await screen.findByLabelText("Outside addresses per list");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("shows the server's reason when it refuses", async () => {
    mockGet.mockResolvedValue({ data: { mail_list_external_member_cap: 25 } });
    mockPatch.mockRejectedValue({ response: { data: { message: "must be between 0 and 1000" } } });
    render(<MailListsCard />);
    await screen.findByLabelText("Outside addresses per list");

    setCap("40");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Not saved: must be between 0 and 1000"));
  });
});

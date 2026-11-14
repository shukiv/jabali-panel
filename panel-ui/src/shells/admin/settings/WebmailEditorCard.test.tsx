// WebmailEditorCard.test.tsx — GH #2501: webmail's WOPI document editor in
// Server Settings → Email. The card saves the entered URL and shows the
// server's reason when it refuses one.
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
import { WebmailEditorCard } from "./WebmailToggleCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

const serve = (url = "") => mockGet.mockResolvedValue({ data: { webmail_office_editor_url: url } });

describe("WebmailEditorCard", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows the stored editor address", async () => {
    serve("https://office.example.com");
    render(<WebmailEditorCard />);
    expect(await screen.findByPlaceholderText("https://office.example.com")).toHaveValue("https://office.example.com");
    expect(mockGet).toHaveBeenCalledWith("/admin/settings");
  });

  it("PATCHes the entered value", async () => {
    serve();
    mockPatch.mockResolvedValue({ data: { webmail_office_editor_url: "https://docs.example.com" } });
    render(<WebmailEditorCard />);
    const input = await screen.findByPlaceholderText("https://office.example.com");
    fireEvent.change(input, { target: { value: "https://docs.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mockPatch).toHaveBeenCalledWith("/admin/settings", { webmail_office_editor_url: "https://docs.example.com" }),
    );
    expect(toast.success).toHaveBeenCalled();
  });

  it("shows the API error on 400", async () => {
    serve();
    mockPatch.mockRejectedValue(
      Object.assign(new Error("Request failed"), {
        isAxiosError: true,
        response: {
          status: 400,
          data: { error: "invalid_settings", detail: "webmail_office_editor_url: must be empty or an https address" },
        },
      }),
    );
    render(<WebmailEditorCard />);
    const input = await screen.findByPlaceholderText("https://office.example.com");
    fireEvent.change(input, { target: { value: "http://docs.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("webmail_office_editor_url: must be empty or an https address"),
    );
  });
});
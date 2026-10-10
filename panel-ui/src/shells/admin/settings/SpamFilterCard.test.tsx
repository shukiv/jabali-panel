// SpamFilterCard.test.tsx — GH #2017: the mail server's spam thresholds in
// Server Settings → Email. Reject and discard can be turned off (sent as 0),
// a threshold at or below the junk one is refused before saving, and the
// server's reason is shown when it refuses.
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
import { SpamFilterCard } from "./SpamFilterCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

const base = { spam_junk_score: 5, spam_reject_score: 15, spam_discard_score: 20, mail_enabled: true };
const serve = (over: Partial<typeof base> = {}) => mockGet.mockResolvedValue({ data: { ...base, ...over } });

const setNumber = (label: string, value: string) => {
  const input = screen.getByLabelText(label);
  fireEvent.change(input, { target: { value } });
  fireEvent.blur(input);
};

describe("SpamFilterCard", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows the stored thresholds", async () => {
    serve();
    render(<SpamFilterCard />);
    expect(await screen.findByLabelText("Junk threshold")).toHaveValue("5.0");
    expect(screen.getByLabelText("Reject threshold")).toHaveValue("15.0");
    expect(screen.getByLabelText("Discard threshold")).toHaveValue("20.0");
    expect(mockGet).toHaveBeenCalledWith("/admin/settings");
  });

  it("saves the edited thresholds", async () => {
    serve();
    mockPatch.mockResolvedValue({ data: { ...base, spam_junk_score: 8 } });
    render(<SpamFilterCard />);
    await screen.findByLabelText("Junk threshold");
    setNumber("Junk threshold", "8");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mockPatch).toHaveBeenCalledWith("/admin/settings", {
        spam_junk_score: 8,
        spam_reject_score: 15,
        spam_discard_score: 20,
      }),
    );
    expect(toast.success).toHaveBeenCalled();
  });

  it("sends 0 for a threshold that is turned off", async () => {
    serve();
    mockPatch.mockResolvedValue({ data: { ...base, spam_reject_score: 0 } });
    render(<SpamFilterCard />);
    await screen.findByLabelText("Junk threshold");
    fireEvent.click(screen.getByRole("switch", { name: "Reject spam" }));
    expect(screen.getByLabelText("Reject threshold")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mockPatch).toHaveBeenCalledWith("/admin/settings", {
        spam_junk_score: 5,
        spam_reject_score: 0,
        spam_discard_score: 20,
      }),
    );
  });

  it("shows a stored 0 as off", async () => {
    serve({ spam_reject_score: 0, spam_discard_score: 0 });
    render(<SpamFilterCard />);
    await screen.findByLabelText("Junk threshold");
    expect(screen.getByRole("switch", { name: "Reject spam" })).not.toBeChecked();
    expect(screen.getByRole("switch", { name: "Discard spam" })).not.toBeChecked();
  });

  it("refuses a reject threshold at or below the junk threshold before saving", async () => {
    serve();
    render(<SpamFilterCard />);
    await screen.findByLabelText("Junk threshold");
    setNumber("Junk threshold", "15");
    expect(await screen.findByText(/must be above the Junk threshold/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("explains that a discard threshold at or above the reject one never applies", async () => {
    // Equal counts: reject is checked first.
    serve({ spam_reject_score: 15, spam_discard_score: 15 });
    render(<SpamFilterCard />);
    expect(await screen.findByText(/rejects it before it could be discarded/)).toBeInTheDocument();
  });

  it("says nothing about discard when it sits below the reject threshold", async () => {
    serve({ spam_reject_score: 30, spam_discard_score: 20 });
    render(<SpamFilterCard />);
    await screen.findByLabelText("Junk threshold");
    expect(screen.queryByText(/rejects it before it could be discarded/)).not.toBeInTheDocument();
  });

  it("shows the server's reason when it refuses", async () => {
    serve();
    mockPatch.mockRejectedValue({
      response: { status: 422, data: { error: "validation_failed", detail: "spam_junk_score must be above 0 and at most 100" } },
    });
    render(<SpamFilterCard />);
    await screen.findByLabelText("Junk threshold");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("spam_junk_score must be above 0 and at most 100"));
  });

  it("says the thresholds do nothing while the mail module is off", async () => {
    serve({ mail_enabled: false });
    render(<SpamFilterCard />);
    expect(await screen.findByText(/The mail module is off/)).toBeInTheDocument();
  });
});

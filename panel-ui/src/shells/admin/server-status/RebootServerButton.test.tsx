// GH #1330: the reboot action must confirm first, then POST /system/reboot.
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RebootServerButton } from "./RebootServerButton";

vi.mock("../../../apiClient", () => ({ apiClient: { post: vi.fn() } }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as { post: ReturnType<typeof vi.fn> };

function renderBtn() {
  render(
    <App>
      <RebootServerButton />
    </App>,
  );
}

beforeEach(() => {
  mocked.post.mockReset().mockResolvedValue({ data: { scheduled: true } });
});

describe("GH #1330 — RebootServerButton", () => {
  it("does not POST until the confirm is accepted", async () => {
    renderBtn();
    fireEvent.click(screen.getByRole("button", { name: /reboot server/i }));
    // The confirm modal is open with its own "Reboot now" button.
    await screen.findByRole("button", { name: /reboot now/i });
    expect(mocked.post).not.toHaveBeenCalled();
  });

  it("POSTs /system/reboot after confirming", async () => {
    renderBtn();
    fireEvent.click(screen.getByRole("button", { name: /reboot server/i }));
    const ok = await screen.findByRole("button", { name: /reboot now/i });
    fireEvent.click(ok);
    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/system/reboot"),
    );
  });
});

// RedisAccessCard.flush.test.tsx — GH #2003. The tenant's Redis user can't run
// FLUSHALL, so the card offers a panel-run flush of the tenant's own keys:
// confirm, POST /me/redis-access/flush, toast the count. apiClient and the toast
// are mocked; the real AntD Card and Popconfirm render.
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RedisAccessCard, redisFlushMessage } from "./RedisAccessCard";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};
const toastOK = feedback.message.success as unknown as ReturnType<typeof vi.fn>;
const toastErr = feedback.message.error as unknown as ReturnType<typeof vi.fn>;

function renderCard() {
  render(
    <App>
      <RedisAccessCard />
    </App>,
  );
}

async function confirmFlush() {
  fireEvent.click(screen.getByRole("button", { name: /flush my redis keys/i }));
  fireEvent.click(await screen.findByRole("button", { name: /^flush$/i }));
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("RedisAccessCard flush (GH #2003)", () => {
  it("asks first, then flushes through the panel and shows the count", async () => {
    mocked.post.mockResolvedValue({ data: { deleted: 3, complete: true } });
    renderCard();

    fireEvent.click(screen.getByRole("button", { name: /flush my redis keys/i }));
    expect(mocked.post).not.toHaveBeenCalled();
    expect(await screen.findByText("Delete all your Redis keys?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^flush$/i }));

    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/me/redis-access/flush"));
    await waitFor(() => expect(toastOK).toHaveBeenCalledWith("Deleted 3 keys."));
    // The flush doesn't need the credentials revealed.
    expect(mocked.get).not.toHaveBeenCalled();
  });

  it("says so when the flush fails", async () => {
    mocked.post.mockRejectedValue(new Error("boom"));
    renderCard();
    await confirmFlush();
    await waitFor(() => expect(toastErr).toHaveBeenCalledWith("Could not flush your Redis keys."));
  });

  it("tells the user to run it again when the flush ran out of time", () => {
    expect(redisFlushMessage({ deleted: 1, complete: true })).toBe("Deleted 1 key.");
    expect(redisFlushMessage({ deleted: 0, complete: true })).toBe("Deleted 0 keys.");
    expect(redisFlushMessage({ deleted: 5000, complete: false })).toMatch(/so far.*again/);
  });
});

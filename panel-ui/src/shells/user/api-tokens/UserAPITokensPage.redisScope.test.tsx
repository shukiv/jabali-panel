// UserAPITokensPage.redisScope.test.tsx — GH #2003. An app that flushes its
// Redis keys gets a token that can do only that: the custom-permissions list
// has a Redis row with a single Flush checkbox (no Read, since reading would
// mean the Redis password), and it mints write:redis.
import { App } from "antd";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UserAPITokensPage } from "./UserAPITokensPage";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

beforeEach(() => {
  vi.clearAllMocks();
  mocked.get.mockResolvedValue({ data: { items: [] } });
  mocked.post.mockResolvedValue({
    data: { secret: "jat_x", token: { id: "t1", name: "cache flush", scopes: ["write:redis"] } },
  });
});

describe("API token Redis flush permission (GH #2003)", () => {
  it("offers Redis as flush-only and mints write:redis", async () => {
    render(
      <App>
        <UserAPITokensPage />
      </App>,
    );
    fireEvent.click(await screen.findByRole("button", { name: /new token/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "cache flush" } });
    fireEvent.click(within(dialog).getByLabelText("Custom"));

    const row = within(dialog).getByText("Redis").parentElement as HTMLElement;
    expect(within(row).queryByLabelText("Read")).toBeNull();
    fireEvent.click(within(row).getByLabelText("Flush"));

    fireEvent.click(within(dialog).getByRole("button", { name: /^create$/i }));
    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/me/api-tokens", { name: "cache flush", scopes: ["write:redis"] }),
    );
  });
});

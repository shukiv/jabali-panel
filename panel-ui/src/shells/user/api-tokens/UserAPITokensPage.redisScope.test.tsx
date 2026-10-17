// UserAPITokensPage.redisScope.test.tsx — GH #2003, #2165. An app that works
// with its Redis keys gets a token that can do only that: the custom-permissions
// list has a Redis row with "List keys" (read:redis) and "Delete keys"
// (write:redis). Neither reveals the Redis password.
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

async function openCustom() {
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
  return { dialog, row };
}

describe("API token Redis permissions (GH #2003, #2165)", () => {
  it("mints write:redis from Delete keys", async () => {
    const { dialog, row } = await openCustom();
    expect(within(row).queryByLabelText("Read")).toBeNull();
    fireEvent.click(within(row).getByLabelText("Delete keys"));
    fireEvent.click(within(dialog).getByRole("button", { name: /^create$/i }));
    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/me/api-tokens", { name: "cache flush", scopes: ["write:redis"] }),
    );
  });

  it("mints read:redis from List keys", async () => {
    const { dialog, row } = await openCustom();
    fireEvent.click(within(row).getByLabelText("List keys"));
    fireEvent.click(within(dialog).getByRole("button", { name: /^create$/i }));
    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/me/api-tokens", { name: "cache flush", scopes: ["read:redis"] }),
    );
  });
});

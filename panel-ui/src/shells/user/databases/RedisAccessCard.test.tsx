// RedisAccessCard.test.tsx — GH #1016. Locks the reveal flow: the card fetches
// /me/redis-access only when asked, and renders the scoped credential + the
// key-prefix instruction. Only apiClient is mocked; the real AntD Card/
// Descriptions/Alert run so a runtime React/AntD break surfaces here.
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RedisAccessCard } from "./RedisAccessCard";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

beforeEach(() => {
  vi.clearAllMocks();
});

describe("RedisAccessCard", () => {
  it("reveals scoped credentials on demand and shows the required key prefix", async () => {
    mocked.get.mockResolvedValue({
      data: {
        socket: "/run/redis/redis.sock",
        host: "",
        port: 0,
        username: "t_bob",
        password: "deadbeef",
        database: 0,
        key_prefix: "jt:bob:",
        allowed_commands: ["+GET", "+SET"],
        note: "socket only",
      },
    });

    render(
      <App>
        <RedisAccessCard />
      </App>,
    );

    // Nothing fetched until the user asks.
    expect(mocked.get).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /show my redis credentials/i }));

    await waitFor(() => {
      expect(mocked.get).toHaveBeenCalledWith("/me/redis-access");
    });

    // Username, socket, and the required prefix all render.
    expect(await screen.findByText("t_bob")).toBeInTheDocument();
    expect(screen.getByText("/run/redis/redis.sock")).toBeInTheDocument();
    // "jt:bob:" appears both in the Descriptions and the prefix Alert.
    expect(screen.getAllByText("jt:bob:").length).toBeGreaterThan(0);
    expect(screen.getByText(/Set your client's key prefix/i)).toBeInTheDocument();
  });
});

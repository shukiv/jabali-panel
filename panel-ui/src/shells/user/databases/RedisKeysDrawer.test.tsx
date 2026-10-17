// RedisKeysDrawer.test.tsx — GH #2165. The tenant's own Redis keys, listed by
// the panel page by page (next_cursor), searchable and filterable by database,
// with single and bulk delete. apiClient and the toasts are mocked; the real
// AntD Drawer, Table and Popconfirm render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { setPanelClock } from "../../../utils/datetime";
import { RedisAccessCard } from "./RedisAccessCard";
import { RedisKeysDrawer, searchMatch, type RedisKeyRow } from "./RedisKeysDrawer";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};
const toastOK = feedback.message.success as unknown as ReturnType<typeof vi.fn>;

const row = (db: number, name: string, extra: Partial<RedisKeyRow> = {}): RedisKeyRow => ({
  db,
  key: `jt:bob:${name}`,
  name,
  type: "string",
  ttl_ms: -1,
  expires_at: null,
  ...extra,
});

function page(data: RedisKeyRow[], next_cursor = "") {
  return { status: 200, data: { data, next_cursor } };
}

function renderDrawer() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <RedisKeysDrawer open onClose={() => {}} />
      </App>
    </QueryClientProvider>,
  );
}

const lastParams = () => mocked.get.mock.calls[mocked.get.mock.calls.length - 1][1]?.params;

beforeEach(() => {
  vi.clearAllMocks();
  setPanelClock("UTC", "DD/MM/YY HH:mm");
});
afterEach(() => cleanup());

describe("RedisKeysDrawer (GH #2165)", () => {
  it("lists the keys with database, type and expiry", async () => {
    mocked.get.mockResolvedValueOnce(
      page([
        row(0, "session:1"),
        row(3, "cache:2", { type: "hash", ttl_ms: 90_000, expires_at: "2026-10-17T06:36:12Z" }),
      ]),
    );
    renderDrawer();

    expect(await screen.findByText("session:1")).toBeInTheDocument();
    expect(mocked.get).toHaveBeenCalledWith("/me/redis-access/keys", { params: { limit: 200 } });
    const cacheRow = screen.getByText("cache:2").closest("tr") as HTMLElement;
    expect(within(cacheRow).getByText("hash")).toBeInTheDocument();
    expect(within(cacheRow).getByText("3")).toBeInTheDocument();
    // The expiry goes through the panel clock, with seconds.
    expect(within(cacheRow).getByText("17/10/26 06:36:12")).toBeInTheDocument();
    const sessionRow = screen.getByText("session:1").closest("tr") as HTMLElement;
    expect(within(sessionRow).getByText("Never")).toBeInTheDocument();
    expect(screen.getByText("2 keys.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /load more/i })).toBeNull();
  });

  it("loads the next page from next_cursor", async () => {
    mocked.get.mockResolvedValueOnce(page([row(0, "a")], "0:55")).mockResolvedValueOnce(page([row(1, "b")]));
    renderDrawer();
    await screen.findByText("a");
    expect(screen.getByText(/1 key so far/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /load more/i }));
    await screen.findByText("b");
    expect(lastParams()).toEqual({ limit: 200, cursor: "0:55" });
    expect(screen.getByText("2 keys.")).toBeInTheDocument();
  });

  it("keeps scanning by itself past pages that found nothing", async () => {
    mocked.get
      .mockResolvedValueOnce(page([], "0:10"))
      .mockResolvedValueOnce(page([], "0:20"))
      .mockResolvedValueOnce(page([row(2, "late")]));
    renderDrawer();
    expect(await screen.findByText("late")).toBeInTheDocument();
    expect(mocked.get).toHaveBeenCalledTimes(3);
  });

  it("searches names literally and filters by database", async () => {
    mocked.get.mockResolvedValue(page([row(0, "a*b")]));
    renderDrawer();
    await screen.findByText("a*b");

    const search = screen.getByLabelText("Search key names");
    fireEvent.change(search, { target: { value: "a*b" } });
    fireEvent.keyDown(search, { key: "Enter", code: "Enter", keyCode: 13 });
    await waitFor(() => expect(lastParams()).toEqual({ limit: 200, match: "*a\\*b*" }));

    fireEvent.mouseDown(screen.getByLabelText("Database"));
    fireEvent.click(await screen.findByText("Database 3"));
    await waitFor(() => expect(lastParams()).toEqual({ limit: 200, match: "*a\\*b*", db: 3 }));
  });

  it("deletes one key after confirming and drops it from the list", async () => {
    mocked.get.mockResolvedValueOnce(page([row(0, "a"), row(3, "b")]));
    mocked.post.mockResolvedValue({ status: 200, data: { deleted: 1 } });
    renderDrawer();
    const aRow = (await screen.findByText("a")).closest("tr") as HTMLElement;

    fireEvent.click(within(aRow).getByRole("button", { name: "Delete" }));
    expect(mocked.post).not.toHaveBeenCalled();
    expect(await screen.findByText("Delete a?")).toBeInTheDocument();
    const confirm = screen.getAllByRole("button", { name: /^delete$/i }).find((b) => b.closest(".ant-popover"));
    fireEvent.click(confirm as HTMLElement);
    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/me/redis-access/keys/delete", { keys: [{ db: 0, key: "jt:bob:a" }] }),
    );
    await waitFor(() => expect(screen.queryByText("a")).toBeNull());
    expect(screen.getByText("b")).toBeInTheDocument();
  });

  it("deletes the selected keys together", async () => {
    mocked.get.mockResolvedValueOnce(page([row(0, "a"), row(3, "b"), row(0, "c")]));
    mocked.post.mockResolvedValue({ status: 200, data: { deleted: 2 } });
    renderDrawer();
    await screen.findByText("a");

    for (const name of ["a", "b"]) {
      const tr = screen.getByText(name).closest("tr") as HTMLElement;
      fireEvent.click(within(tr).getByRole("checkbox"));
    }
    fireEvent.click(screen.getByRole("button", { name: /delete selected/i }));
    expect(await screen.findByText("Delete 2 keys?")).toBeInTheDocument();
    const confirm = screen.getAllByRole("button", { name: /^delete$/i }).find((b) => b.closest(".ant-popover"));
    fireEvent.click(confirm as HTMLElement);

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/me/redis-access/keys/delete", {
        keys: [
          { db: 0, key: "jt:bob:a" },
          { db: 3, key: "jt:bob:b" },
        ],
      }),
    );
    await waitFor(() => expect(toastOK).toHaveBeenCalledWith("Deleted 2 keys."));
    expect(screen.getByText("c")).toBeInTheDocument();
  });

  it("can't select or delete a key whose name isn't UTF-8", async () => {
    mocked.get.mockResolvedValueOnce(page([row(0, "�x", { binary: true })]));
    renderDrawer();
    const tr = (await screen.findByText("binary")).closest("tr") as HTMLElement;
    expect(within(tr).getByRole("checkbox")).toBeDisabled();
    expect(within(tr).queryByRole("button", { name: "Delete" })).toBeNull();
  });
});

describe("RedisAccessCard key browser (GH #2165)", () => {
  it("opens the key list without fetching the credential", async () => {
    mocked.get.mockResolvedValue(page([row(0, "a")]));
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <App>
          <RedisAccessCard />
        </App>
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: /browse my keys/i }));
    expect(await screen.findByText("My Redis keys")).toBeInTheDocument();
    await screen.findByText("a");
    expect(mocked.get).toHaveBeenCalledTimes(1);
    expect(mocked.get).toHaveBeenCalledWith("/me/redis-access/keys", expect.anything());
  });
});

describe("searchMatch", () => {
  it("matches the text anywhere in the name, glob characters literally", () => {
    expect(searchMatch("")).toBeUndefined();
    expect(searchMatch("  ")).toBeUndefined();
    expect(searchMatch("session")).toBe("*session*");
    expect(searchMatch("a*b?[c]\\")).toBe("*a\\*b\\?\\[c\\]\\\\*");
  });
});

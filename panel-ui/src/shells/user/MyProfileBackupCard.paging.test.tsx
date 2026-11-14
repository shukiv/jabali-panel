// GH #2505: the tenant Backups card pages on the server. The first request
// carries page=1 & page_size=10, and the pagination is driven by the server's
// total — so every backup is reachable, not just the first page the browser
// happened to load.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MyProfileBackupCard } from "./MyProfileBackupCard";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

vi.mock("../../lib/feedback", () => ({
  feedback: {
    modal: { confirm: (o: { onOk?: () => void }) => o.onOk?.() },
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  },
}));

import { apiClient } from "../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function rows(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    id: `b${i}`,
    kind: "account_backup",
    status: "succeeded",
    bytes_total: 100,
    bytes_added: 100,
    created_at: "2026-08-24T00:00:00Z",
  }));
}

beforeEach(() => {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url.startsWith("/me/backups/exclusions")) return Promise.resolve({ data: { patterns: "" } });
    if (url.startsWith("/me/backups/destinations")) return Promise.resolve({ data: { data: [], allow_local: false } });
    if (url.startsWith("/me/backups")) return Promise.resolve({ data: { data: rows(10), total: 62 } });
    return Promise.resolve({ data: { data: [] } });
  });
  mocked.put.mockReset().mockResolvedValue({ data: {} });
  mocked.post.mockReset().mockResolvedValue({ data: {} });
  mocked.delete.mockReset().mockResolvedValue({ data: { status: "ok" } });
});

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <MyProfileBackupCard />
      </App>
    </QueryClientProvider>,
  );
}

describe("MyProfileBackupCard server paging (GH #2505)", () => {
  it("asks for page 1 at size 10 and pages by the server total", async () => {
    renderCard();
    await waitFor(() => expect(mocked.get).toHaveBeenCalledWith("/me/backups?page=1&page_size=10"));
    // 62 backups at 10 per page = 7 pages, though only 10 rows are loaded.
    expect(await screen.findByTitle("7")).toBeTruthy();
    fireEvent.click(screen.getByTitle("2"));
    await waitFor(() => expect(mocked.get).toHaveBeenCalledWith("/me/backups?page=2&page_size=10"));
  });
});

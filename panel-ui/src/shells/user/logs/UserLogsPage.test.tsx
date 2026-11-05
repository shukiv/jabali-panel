// UserLogsPage.test.tsx — GH #2381. The tenant's Logs page had the same
// first-20 limit as the admin's: a domain past the first page was missing,
// and a ?domain= link to it showed the wrong list.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { get: vi.fn() } }));
vi.mock("../activity/AccountActivity", () => ({ AccountActivity: () => null }));

import { apiClient } from "../../../apiClient";
import { UserLogsPage } from "./UserLogsPage";

const get = (apiClient as unknown as { get: ReturnType<typeof vi.fn> }).get;

const DOMAINS = Array.from({ length: 230 }, (_, i) => ({
  id: `d${i + 1}`,
  name: `site${String(i + 1).padStart(3, "0")}.example`,
}));

beforeEach(() => {
  vi.clearAllMocks();
  get.mockImplementation(async (url: string) => {
    const u = new URL(url, "http://panel.test");
    const page = Number(u.searchParams.get("page") ?? 1);
    const size = Math.min(Number(u.searchParams.get("page_size") ?? 20), 200);
    const start = (page - 1) * size;
    return { data: { data: DOMAINS.slice(start, start + size), total: DOMAINS.length, page, page_size: size } };
  });
});

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={qc}>
        <App>
          <UserLogsPage />
        </App>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("UserLogsPage (GH #2381)", () => {
  it("a ?domain= link opens a domain past the first page", async () => {
    renderAt("/logs?domain=d230");
    expect(await screen.findByText("site230.example")).toBeTruthy();
    await waitFor(() => expect(screen.queryByText("site001.example")).toBeNull());
  });
});

// DomainLogsTab.test.tsx — GH #2381. Logs & Statistics listed only the first
// 20 domains: it asked /domains for no page size and got the API's first page.
// It now reads every page, and a search box narrows the list by name.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { get: vi.fn() } }));

import { apiClient } from "../../../apiClient";
import { DomainLogsTab } from "./DomainLogsTab";

const get = (apiClient as unknown as { get: ReturnType<typeof vi.fn> }).get;

// 230 domains, more than the largest page the API hands out (200).
const DOMAINS = Array.from({ length: 230 }, (_, i) => ({
  id: `d${i + 1}`,
  name: `site${String(i + 1).padStart(3, "0")}.example`,
}));

// Pages like the real endpoint: 20 rows unless page_size asks for more.
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

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainLogsTab />
      </App>
    </QueryClientProvider>,
  );
}

describe("DomainLogsTab (GH #2381)", () => {
  it("reads every page of /domains, not just the first", async () => {
    renderTab();
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    const urls = get.mock.calls.map(([u]) => String(u));
    expect(urls.some((u) => u.includes("page=2"))).toBe(true);
  });

  it("finds a domain past the first 200 by name", async () => {
    renderTab();
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    fireEvent.change(screen.getByPlaceholderText("Search domains…"), { target: { value: "site230" } });
    expect(await screen.findByText("site230.example")).toBeTruthy();
    expect(screen.queryByText("site001.example")).toBeNull();
    // The aggregate row stays on top whatever the search.
    expect(screen.getByText("All Domains")).toBeTruthy();
  });
});

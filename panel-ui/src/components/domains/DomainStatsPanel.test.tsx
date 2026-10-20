// DomainStatsPanel.test — the domain Statistics tab: window totals, the
// visitors-by-country table with shares, and the empty states for a domain
// with no traffic counted yet and a server without the GeoIP database.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn() },
}));

import { apiClient } from "../../apiClient";
import { DomainStatsPanel } from "./DomainStatsPanel";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DomainStatsPanel domainId="d1" />
    </QueryClientProvider>,
  );
}

const withTraffic = {
  from: "2026-09-20",
  to: "2026-10-19",
  bytes_total: 4631606,
  requests_total: 1443,
  daily: [
    { day: "2026-10-18T00:00:00Z", bytes_total: 1000, requests_total: 40 },
    { day: "2026-10-19T00:00:00Z", bytes_total: 4630606, requests_total: 1403 },
  ],
  countries: [
    { country_code: "US", bytes_total: 2658828, requests_total: 300 },
    { country_code: "DE", bytes_total: 6650, requests_total: 100 },
    { country_code: "ZZ", bytes_total: 500, requests_total: 100 },
  ],
};

beforeEach(() => {
  vi.clearAllMocks();
});

describe("DomainStatsPanel", () => {
  it("asks for the last 30 days, ending yesterday", async () => {
    mockGet.mockResolvedValue({ data: withTraffic });
    renderPanel();
    await waitFor(() => expect(mockGet).toHaveBeenCalled());
    const [url, opts] = mockGet.mock.calls[0];
    expect(url).toBe("/domains/d1/bandwidth");
    const day = 86_400_000;
    const iso = (ms: number) => new Date(ms).toISOString().slice(0, 10);
    expect(opts.params).toEqual({ from: iso(Date.now() - 30 * day), to: iso(Date.now() - day) });
  });

  it("shows the totals and each country with its share", async () => {
    mockGet.mockResolvedValue({ data: withTraffic });
    renderPanel();
    expect(await screen.findByText("1,443")).toBeInTheDocument();
    expect(screen.getAllByText("United States").length).toBeGreaterThan(0);
    expect(screen.getByText("Germany")).toBeInTheDocument();
    expect(screen.getByText("Unknown / other")).toBeInTheDocument();
    // 300 of the 500 country requests.
    expect(screen.getByText("60% of visitor requests")).toBeInTheDocument();
    expect(screen.getByText(/includes GeoLite2 data created by MaxMind/)).toBeInTheDocument();
  });

  it("switching the window asks for that window", async () => {
    mockGet.mockResolvedValue({ data: withTraffic });
    renderPanel();
    await screen.findByText("1,443");
    fireEvent.click(screen.getByText("Last 7 days"));
    await waitFor(() => expect(mockGet).toHaveBeenCalledTimes(2));
    const day = 86_400_000;
    expect(mockGet.mock.calls[1][1].params.from).toBe(new Date(Date.now() - 7 * day).toISOString().slice(0, 10));
  });

  it("names countries in a language whose code has an underscore", async () => {
    const { default: i18n } = await import("i18next");
    const prev = i18n.language;
    await i18n.changeLanguage("pt_BR");
    try {
      mockGet.mockResolvedValue({ data: withTraffic });
      renderPanel();
      expect(await screen.findByText("Alemanha")).toBeInTheDocument();
    } finally {
      await i18n.changeLanguage(prev);
    }
  });

  it("says when nothing has been counted and when there is no country data", async () => {
    mockGet.mockResolvedValue({
      data: { ...withTraffic, bytes_total: 0, requests_total: 0, daily: [], countries: [] },
    });
    renderPanel();
    expect(await screen.findByText(/No traffic counted yet/)).toBeInTheDocument();
    expect(screen.getByText(/No country data for this period/)).toBeInTheDocument();
    expect(screen.queryByText(/MaxMind/)).not.toBeInTheDocument();
  });
});

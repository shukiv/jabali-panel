// GH #1540: the admin DNS Zones list gets an "Add DNS Zone" button that opens
// the admin drawer (Owner picker + Domain Name + IP + Template).
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (k: string) => k }) }));
vi.mock("react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({
    data: { mail_enabled: true, dns_enabled: true, public_ipv4: "192.0.2.1" },
  }),
}));
vi.mock("../../../hooks/useQueries", () => ({
  useCreateMutation: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn().mockResolvedValue({ data: { data: [] } }) },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
    modal: { success: vi.fn() },
  },
}));
vi.mock("../../../components/EmptyWithCTA", () => ({ EmptyWithCTA: () => <div /> }));
// Stub the inventory — this adapter test only cares that the header action
// (Add DNS Zone) is rendered and wired to the admin drawer.
vi.mock("../../../components/dns/DNSZoneInventory", () => ({
  DnsZoneInventory: ({ audience }: { audience: { header: { extra?: ReactNode } } }) => (
    <div>{audience.header.extra}</div>
  ),
}));

import { DNSZonesOverviewPage } from "./DNSZonesOverviewPage";

describe("admin DNSZonesOverviewPage Add DNS Zone (GH #1540)", () => {
  it("opens the admin Add DNS Zone drawer with owner, domain and prefilled IP", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <DNSZonesOverviewPage />
      </QueryClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: /add dns zone/i }));
    expect(await screen.findByText("Owner")).toBeInTheDocument();
    expect(await screen.findByPlaceholderText("e.g., example.com")).toBeInTheDocument();
    const ip = (await screen.findByPlaceholderText("e.g., 203.0.113.10")) as HTMLInputElement;
    expect(ip.value).toBe("192.0.2.1");
  });
});

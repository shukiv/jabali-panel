// GH #1541: DNS-only zones are added from the DNS Zones page now (the Domains
// "Add" split is gone). The page's "Add DNS Zone" button must open the shared
// Add-domain drawer in dns mode.
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { mail_enabled: true, dns_enabled: true } }),
}));
vi.mock("../../../hooks/useQueries", () => ({
  useCreateMutation: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
    modal: { success: vi.fn(), confirm: vi.fn() },
  },
}));
// Stub the inventory — this adapter test only cares that the header action
// (Add DNS Zone) is rendered and wired to the drawer.
vi.mock("../../../components/dns/DNSZoneInventory", () => ({
  DnsZoneInventory: ({ audience }: { audience: { header: { extra?: ReactNode } } }) => (
    <div>{audience.header.extra}</div>
  ),
}));

import { UserDNSZonesOverviewPage } from "./UserDNSZonesOverviewPage";

describe("UserDNSZonesOverviewPage Add DNS Zone (GH #1541)", () => {
  it("opens the Add-domain drawer in dns mode", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <UserDNSZonesOverviewPage />
      </QueryClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: /add dns zone/i }));
    // The drawer (dns mode) shows the domain-name input.
    expect(await screen.findByPlaceholderText("e.g., example.com")).toBeInTheDocument();
  });
});

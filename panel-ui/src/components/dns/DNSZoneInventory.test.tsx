// DNSZoneInventory.test.tsx — JAB-299. The shared DNS Zone Inventory Module
// must honor the audience policy: the admin audience keeps the owner column
// and admin routes, the tenant audience drops the owner column and uses tenant
// routes, and the DNSSEC tab receives the audience's owner-visibility policy
// (AC4 / AC5). The common columns render the same values for both (AC3).
import { App } from "antd";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

import {
  DnsZoneInventory,
  type DnsZoneInventoryAudience,
  type DnsZoneRow,
} from "./DNSZoneInventory";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const navigateSpy = vi.fn();
vi.mock("react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router")>();
  return { ...actual, useNavigate: () => navigateSpy };
});

// DNSSECTable is exercised by its own tests; here we only need to capture the
// owner-visibility policy the audience hands it (AC5).
vi.mock("../dnssec/DNSSECTable", () => ({
  DNSSECTable: ({ showOwner }: { showOwner: boolean }) => (
    <div data-testid="dnssec-table" data-showowner={String(showOwner)} />
  ),
}));

const provisioned: DnsZoneRow = {
  id: "d1",
  user_id: "u1234567890",
  username: "alice",
  name: "one.tld",
  provisioned: true,
  record_count: 3,
  effective_ttl: 300,
  dnssec_enabled: true,
  registrar_expires_at: null,
};

const notProvisioned: DnsZoneRow = {
  id: "d2",
  user_id: "u2",
  username: "bob",
  name: "two.tld",
  provisioned: false,
  record_count: 0,
  effective_ttl: null,
  dnssec_enabled: false,
  registrar_expires_at: null,
};

vi.mock("../../hooks/useTableURL", () => ({
  useTableURL: () => ({
    items: [provisioned, notProvisioned],
    total: 2,
    isLoading: false,
    isError: false,
    params: { page: 1, pageSize: 20, q: "", sort: "name", order: "asc" },
    setParams: vi.fn(),
  }),
}));

const adminAudience: DnsZoneInventoryAudience = {
  showOwner: true,
  manageRoute: (id) => `/jabali-admin/domains/${id}/dns`,
  renderEmpty: () => <div>empty</div>,
  dnssec: { showOwner: true, message: "m", description: "d" },
  header: { icon: null, title: "DNS Zones" },
};

const tenantAudience: DnsZoneInventoryAudience = {
  showOwner: false,
  manageRoute: (id) => `/jabali-panel/domains/${id}/dns`,
  renderEmpty: () => <div>empty</div>,
  dnssec: { showOwner: false, message: "m", description: "d" },
  header: { icon: null, title: "DNS" },
};

const renderPage = (audience: DnsZoneInventoryAudience) =>
  render(
    <MemoryRouter>
      <App>
        <DnsZoneInventory audience={audience} />
      </App>
    </MemoryRouter>,
  );

describe("DnsZoneInventory audience policy (JAB-299)", () => {
  it("admin audience shows the owner column and routes to admin domains (AC4)", () => {
    renderPage(adminAudience);

    // Owner column header + owner values are admin-only. antd renders the
    // sortable header text in more than one node, so assert at least one.
    expect(screen.getAllByText("dnszonesoverviewpage.owner").length).toBeGreaterThan(0);
    expect(screen.getByText("alice")).toBeInTheDocument();
    expect(screen.getByText("bob")).toBeInTheDocument();

    // Manage Records on the first row navigates to the admin route.
    fireEvent.click(screen.getAllByText("Manage Records")[0]);
    expect(navigateSpy).toHaveBeenCalledWith("/jabali-admin/domains/d1/dns");
  });

  it("tenant audience drops the owner column and routes to tenant domains (AC4)", () => {
    navigateSpy.mockClear();
    renderPage(tenantAudience);

    // No owner column, no owner values.
    expect(screen.queryByText("dnszonesoverviewpage.owner")).not.toBeInTheDocument();
    expect(screen.queryByText("alice")).not.toBeInTheDocument();

    fireEvent.click(screen.getAllByText("Manage Records")[0]);
    expect(navigateSpy).toHaveBeenCalledWith("/jabali-panel/domains/d1/dns");
  });

  it("renders provisioning, DNSSEC, and TTL presentation for both rows (AC3)", () => {
    renderPage(adminAudience);

    expect(screen.getByText("Provisioned")).toBeInTheDocument();
    expect(screen.getByText("Not provisioned")).toBeInTheDocument();
    expect(screen.getByText("Signed")).toBeInTheDocument();
    expect(screen.getByText("Unsigned")).toBeInTheDocument();
    expect(screen.getByText("300s")).toBeInTheDocument();
    // effective_ttl null and registrar_expires_at null both render an em dash.
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("DNSSEC tab receives showOwner=true for the admin audience (AC5)", () => {
    renderPage(adminAudience);
    fireEvent.click(screen.getByText("DNSSEC"));
    expect(screen.getByTestId("dnssec-table").getAttribute("data-showowner")).toBe("true");
  });

  it("DNSSEC tab receives showOwner=false for the tenant audience (AC5)", () => {
    renderPage(tenantAudience);
    fireEvent.click(screen.getByText("DNSSEC"));
    expect(screen.getByTestId("dnssec-table").getAttribute("data-showowner")).toBe("false");
  });
});

// DNSZoneInventory.test.tsx — JAB-299. The shared DNS Zone Inventory Module
// must honor the audience policy: the admin audience keeps the owner column
// and admin routes, the tenant audience drops the owner column and uses tenant
// routes, and the DNSSEC tab receives the audience's owner-visibility policy
// (AC4 / AC5). The common columns render the same values for both (AC3).
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

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

// Mutable so a test can drive different row states (GH #1611 adds Enable /
// Delete-domain branches). vi.hoisted lets the hoisted vi.mock factory read it.
const table = vi.hoisted(() => ({ items: [] as DnsZoneRow[] }));
vi.mock("../../hooks/useTableURL", () => ({
  useTableURL: () => ({
    items: table.items,
    total: table.items.length,
    isLoading: false,
    isError: false,
    params: { page: 1, pageSize: 20, q: "", sort: "name", order: "asc" },
    setParams: vi.fn(),
  }),
}));

beforeEach(() => {
  table.items = [provisioned, notProvisioned];
});

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
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <App>
          <DnsZoneInventory audience={audience} />
        </App>
      </MemoryRouter>
    </QueryClientProvider>,
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

  it("shows a DNS-zone delete action gated by provisioning + DNSSEC (GH #1611)", () => {
    renderPage(adminAudience);

    // Only the provisioned row carries a delete action; the not-provisioned row
    // (nothing to tear down) has none.
    const del = screen.getAllByText("dnszonesoverviewpage.delete_zone");
    expect(del.length).toBe(1);
    // The provisioned fixture is DNSSEC-signed, so the button is disabled
    // (unsign first) — the same refusal the backend enforces.
    expect(del[0].closest("button")).toBeDisabled();
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

describe("DnsZoneInventory facet-state actions (GH #1611)", () => {
  // dns_disabled: DNS deliberately dropped — no zone row (provisioned=false).
  const dnsDropped: DnsZoneRow = {
    id: "d3",
    user_id: "u3",
    username: "carol",
    name: "dropped.tld",
    provisioned: false,
    record_count: 0,
    effective_ttl: null,
    dnssec_enabled: false,
    registrar_expires_at: null,
    dns_disabled: true,
    web_disabled: false,
    email_enabled: true,
  };
  // DNS-only: web off + mail off, zone provisioned. The zone can't be dropped
  // alone (last facet), so the row offers a whole-domain delete.
  const dnsOnly: DnsZoneRow = {
    id: "d4",
    user_id: "u4",
    username: "dave",
    name: "dnsonly.tld",
    provisioned: true,
    record_count: 5,
    effective_ttl: 300,
    dnssec_enabled: false,
    registrar_expires_at: null,
    dns_disabled: false,
    web_disabled: true,
    email_enabled: false,
  };
  // Ordinary multi-facet, provisioned, unsigned → an enabled zone delete.
  const normalMulti: DnsZoneRow = {
    id: "d5",
    user_id: "u5",
    username: "erin",
    name: "multi.tld",
    provisioned: true,
    record_count: 4,
    effective_ttl: 300,
    dnssec_enabled: false,
    registrar_expires_at: null,
    dns_disabled: false,
    web_disabled: false,
    email_enabled: true,
  };

  it("a dropped-DNS row shows 'Hosted elsewhere' + Enable DNS and hides Manage Records", () => {
    table.items = [dnsDropped];
    renderPage(adminAudience);

    expect(screen.getByText("dnszonesoverviewpage.dns_hosted_elsewhere")).toBeInTheDocument();
    expect(screen.getByText("dnszonesoverviewpage.enable_dns")).toBeInTheDocument();
    // No zone rows exist while DNS is dropped, so no "Manage Records" and no
    // zone/domain delete on this row.
    expect(screen.queryByText("Manage Records")).not.toBeInTheDocument();
    expect(screen.queryByText("dnszonesoverviewpage.delete_zone")).not.toBeInTheDocument();
    expect(screen.queryByText("dnszonesoverviewpage.delete_domain")).not.toBeInTheDocument();
  });

  it("a DNS-only row offers Delete domain, not a zone delete", () => {
    table.items = [dnsOnly];
    renderPage(adminAudience);

    expect(screen.getByText("dnszonesoverviewpage.delete_domain")).toBeInTheDocument();
    expect(screen.queryByText("dnszonesoverviewpage.delete_zone")).not.toBeInTheDocument();
    // A DNS-only row still has a zone to manage.
    expect(screen.getByText("Manage Records")).toBeInTheDocument();
  });

  it("an ordinary multi-facet row keeps the zone delete; the three states coexist", () => {
    table.items = [dnsDropped, dnsOnly, normalMulti];
    renderPage(adminAudience);

    // Each state renders exactly its own action.
    expect(screen.getAllByText("dnszonesoverviewpage.enable_dns")).toHaveLength(1);
    expect(screen.getAllByText("dnszonesoverviewpage.delete_domain")).toHaveLength(1);
    expect(screen.getAllByText("dnszonesoverviewpage.delete_zone")).toHaveLength(1);
    // Manage Records on both provisioned/manageable rows, hidden on the dropped one.
    expect(screen.getAllByText("Manage Records")).toHaveLength(2);
  });
});

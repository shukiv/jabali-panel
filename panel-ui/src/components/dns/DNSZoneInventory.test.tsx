// DNSZoneInventory.test.tsx — JAB-299 / GH #1611 / GH #1918. The shared DNS
// Zone Inventory Module must honor the audience policy: the admin audience
// keeps the owner column and admin routes, the tenant audience drops the owner
// column and uses tenant routes (AC4). The common columns render the same
// values for both (AC3).
//
// GH #1918 (johnnyq): there is no DNSSEC tab and no "Manage Records" button.
// The domain name links to the zone's records, and the row's ⋯ menu holds
// Enable / Disable DNSSEC (Disable confirms first), View DS & keys, and the
// zone or domain delete.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
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

const fb = vi.hoisted(() => ({
  confirm: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));
vi.mock("../../lib/feedback", () => ({
  feedback: {
    message: { success: fb.success, error: fb.error, warning: vi.fn() },
    modal: { confirm: fb.confirm },
  },
}));

// The DNSSEC endpoints: the flip mutation is a spy, and the keys modal reads a
// signed state with one KSK and one DS record.
const dnssec = vi.hoisted(() => ({ mutateAsync: vi.fn() }));
vi.mock("../../hooks/useDNSSEC", () => ({
  useSetDNSSEC: () => ({ mutateAsync: dnssec.mutateAsync, isPending: false }),
  useDNSSECState: (id: string | undefined) => ({
    isLoading: false,
    isError: false,
    data: id
      ? {
          domain_id: id,
          domain_name: "one.tld",
          enabled: true,
          keys: [{ key_tag: 4242, key_type: "KSK", algorithm: 13, public_key: "k", active: true }],
        }
      : undefined,
  }),
  useDSRecords: (id: string | undefined, enabled: boolean) => ({
    isLoading: false,
    isError: false,
    data:
      id && enabled
        ? { domain_id: id, domain_name: "one.tld", ds_records: [{ key_tag: 4242, algorithm: 13, digest_type: 2, digest: "ABCDEF0123" }] }
        : undefined,
  }),
  algorithmLabel: (a: number) => `alg${a}`,
  digestTypeLabel: (d: number) => `dt${d}`,
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
  dnssecNote: "admin signing note",
  header: { icon: null, title: "DNS Zones" },
};

const tenantAudience: DnsZoneInventoryAudience = {
  showOwner: false,
  manageRoute: (id) => `/jabali-panel/domains/${id}/dns`,
  renderEmpty: () => <div>empty</div>,
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

// rowOf finds a zone's table row by its domain name.
const rowOf = (name: string) => screen.getByText(name).closest("tr") as HTMLElement;

// openMenu opens a row's ⋯ menu and returns the menu items' accessible names.
const openMenu = async (name: string) => {
  fireEvent.click(within(rowOf(name)).getByRole("button", { name: `Actions for ${name}` }));
  const items = await screen.findAllByRole("menuitem");
  return items;
};

const menuItem = (label: RegExp) => screen.getByRole("menuitem", { name: label });

describe("DnsZoneInventory audience policy (JAB-299)", () => {
  it("admin audience shows the owner column and links names to admin domains (AC4)", () => {
    renderPage(adminAudience);

    // Owner column header + owner values are admin-only. antd renders the
    // sortable header text in more than one node, so assert at least one.
    expect(screen.getAllByText("dnszonesoverviewpage.owner").length).toBeGreaterThan(0);
    expect(screen.getByText("alice")).toBeInTheDocument();
    expect(screen.getByText("bob")).toBeInTheDocument();

    // GH #1918: the domain name opens the zone's records.
    expect(screen.getByRole("link", { name: "one.tld" })).toHaveAttribute(
      "href",
      "/jabali-admin/domains/d1/dns",
    );
  });

  it("tenant audience drops the owner column and links names to tenant domains (AC4)", () => {
    renderPage(tenantAudience);

    // No owner column, no owner values.
    expect(screen.queryByText("dnszonesoverviewpage.owner")).not.toBeInTheDocument();
    expect(screen.queryByText("alice")).not.toBeInTheDocument();

    expect(screen.getByRole("link", { name: "one.tld" })).toHaveAttribute(
      "href",
      "/jabali-panel/domains/d1/dns",
    );
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
});

describe("DnsZoneInventory one zone list (GH #1918)", () => {
  beforeEach(() => {
    dnssec.mutateAsync.mockReset();
    dnssec.mutateAsync.mockResolvedValue({});
    fb.confirm.mockReset();
  });

  it("has no DNSSEC tab and no Manage Records button", () => {
    renderPage(adminAudience);

    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    expect(screen.queryByText("Manage Records")).not.toBeInTheDocument();
  });

  it("a signed zone's menu offers View DS & keys and Disable DNSSEC; its delete waits for unsigning", async () => {
    renderPage(adminAudience);
    await openMenu("one.tld");

    expect(menuItem(/View DS & keys/)).toBeInTheDocument();
    expect(menuItem(/Disable DNSSEC/)).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /Enable DNSSEC/ })).not.toBeInTheDocument();
    // The provisioned fixture is DNSSEC-signed, so the zone delete is disabled
    // (unsign first) — the same refusal the backend enforces.
    const del = menuItem(/dnszonesoverviewpage.delete_zone/);
    expect(del).toHaveAttribute("aria-disabled", "true");
    expect(del).toHaveTextContent("dnszonesoverviewpage.delete_disabled_dnssec");
  });

  it("Disable DNSSEC confirms first and only then turns signing off", async () => {
    renderPage(adminAudience);
    await openMenu("one.tld");
    fireEvent.click(menuItem(/Disable DNSSEC/));

    expect(fb.confirm).toHaveBeenCalledTimes(1);
    const opts = fb.confirm.mock.calls[0][0] as { content: string; onOk: () => Promise<void> };
    expect(opts.content).toMatch(/DS record at your registrar/);
    expect(dnssec.mutateAsync).not.toHaveBeenCalled();

    await opts.onOk();
    expect(dnssec.mutateAsync).toHaveBeenCalledWith({ domainID: "d1", enabled: false });
  });

  it("an unsigned provisioned zone offers Enable DNSSEC, which signs without a confirm", async () => {
    table.items = [{ ...provisioned, id: "d9", name: "nine.tld", dnssec_enabled: false }];
    renderPage(adminAudience);
    await openMenu("nine.tld");

    expect(screen.queryByRole("menuitem", { name: /View DS & keys/ })).not.toBeInTheDocument();
    expect(menuItem(/dnszonesoverviewpage.delete_zone/)).not.toHaveAttribute("aria-disabled", "true");
    fireEvent.click(menuItem(/Enable DNSSEC/));

    expect(fb.confirm).not.toHaveBeenCalled();
    expect(dnssec.mutateAsync).toHaveBeenCalledWith({ domainID: "d9", enabled: true });
  });

  it("an unprovisioned zone has nothing to sign or delete, so it has no menu", () => {
    table.items = [notProvisioned];
    renderPage(adminAudience);

    expect(within(rowOf("two.tld")).queryByRole("button", { name: /Actions for/ })).not.toBeInTheDocument();
  });

  it("View DS & keys opens the keys and DS records, with the audience note", async () => {
    renderPage(adminAudience);
    await openMenu("one.tld");
    fireEvent.click(menuItem(/View DS & keys/));

    expect(await screen.findByText("DNSSEC keys · one.tld")).toBeInTheDocument();
    expect(screen.getAllByText("4242").length).toBeGreaterThan(0);
    expect(screen.getByText("ABCDEF0123")).toBeInTheDocument();
    expect(screen.getByText("admin signing note")).toBeInTheDocument();
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

  it("a dropped-DNS row shows 'Hosted elsewhere' + Enable DNS, with no records link or menu", () => {
    table.items = [dnsDropped];
    renderPage(adminAudience);

    expect(screen.getByText("dnszonesoverviewpage.dns_hosted_elsewhere")).toBeInTheDocument();
    expect(screen.getByText("dnszonesoverviewpage.enable_dns")).toBeInTheDocument();
    // No zone rows exist while DNS is dropped: the name is plain text and
    // there is no ⋯ menu (so no zone/domain delete and no DNSSEC).
    expect(screen.queryByRole("link", { name: "dropped.tld" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Actions for/ })).not.toBeInTheDocument();
  });

  it("a DNS-only row offers Delete domain, not a zone delete", async () => {
    table.items = [dnsOnly];
    renderPage(adminAudience);

    // A DNS-only row still has a zone to manage.
    expect(screen.getByRole("link", { name: "dnsonly.tld" })).toBeInTheDocument();
    await openMenu("dnsonly.tld");
    expect(menuItem(/dnszonesoverviewpage.delete_domain/)).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /dnszonesoverviewpage.delete_zone/ })).not.toBeInTheDocument();
  });

  it("an ordinary multi-facet row keeps the zone delete; the three states coexist", async () => {
    table.items = [dnsDropped, dnsOnly, normalMulti];
    renderPage(adminAudience);

    expect(screen.getAllByText("dnszonesoverviewpage.enable_dns")).toHaveLength(1);
    // Records links on both manageable rows, none on the dropped one.
    expect(screen.getAllByRole("link")).toHaveLength(2);
    // ⋯ menus on the two rows with a zone, not on the dropped one.
    expect(screen.getAllByRole("button", { name: /Actions for/ })).toHaveLength(2);
    await openMenu("multi.tld");
    expect(menuItem(/dnszonesoverviewpage.delete_zone/)).not.toHaveAttribute("aria-disabled", "true");
    expect(screen.queryByRole("menuitem", { name: /dnszonesoverviewpage.delete_domain/ })).not.toBeInTheDocument();
  });
});

// domainColumns.test — GH #1543 (D1). The Application column is tenant-only:
// the tenant Web Domains list gains it; the admin list (which edits from its
// own Edit page and has a dedicated Applications list) must not.
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { buildDomainDataColumns, type DomainInventoryAudience } from "./domainColumns";
import type { Domain } from "./types";

const ctx = { t: (k: string) => k, query: { params: {}, setParams: vi.fn() } };
const colKeys = (audience: DomainInventoryAudience) =>
  buildDomainDataColumns(audience, ctx).map((c) => c.key ?? ("dataIndex" in c ? c.dataIndex : undefined));

describe("buildDomainDataColumns Application column (GH #1543)", () => {
  it("adds an Application column on the tenant list", () => {
    expect(colKeys({ kind: "tenant" })).toContain("application");
  });

  it("omits the Application column on the admin list", () => {
    expect(colKeys({ kind: "admin" })).not.toContain("application");
  });
});

// GH #1606 / #1449: the service badge ("DNS only" / "Mail only" / "External
// DNS") must render on the admin list too, not just the tenant one — an
// operator reviewing a migration otherwise cannot tell a docroot-less DNS-only
// zone from a real web domain. The admin name cell renders a plain anchor (no
// react-router), so it renders in isolation without a Router wrapper.
describe("service badge on the name column (GH #1606)", () => {
  const nameCell = (audience: DomainInventoryAudience, record: Domain): ReactNode => {
    const col = buildDomainDataColumns(audience, ctx).find((c) => c.key === "name");
    const r = (col as { render: (v: unknown, rec: Domain, i: number) => ReactNode }).render;
    return r("", record, 0);
  };
  const dnsOnly = { name: "dns.example.com", doc_root: "", web_disabled: true, email_enabled: false } as Domain;
  const fullWeb = { name: "web.example.com", doc_root: "/home/u/web", web_disabled: false, dns_disabled: false, email_enabled: true } as Domain;

  it("renders the DNS-only badge on the admin list", () => {
    render(<>{nameCell({ kind: "admin", ownerId: "u1" }, dnsOnly)}</>);
    expect(screen.getByText("DNS only")).toBeTruthy();
  });

  it("shows no badge for a full-service web domain on the admin list", () => {
    render(<>{nameCell({ kind: "admin", ownerId: "u1" }, fullWeb)}</>);
    expect(screen.queryByText("DNS only")).toBeNull();
    expect(screen.queryByText("Mail only")).toBeNull();
    expect(screen.queryByText("External DNS")).toBeNull();
  });
});

// GH #1625 (johnnyq): a domain with web aliases lists them under its name in
// place of the document root, which stays in the line's tooltip.
describe("aliases on the name column (GH #1625)", () => {
  const nameCell = (record: Domain): ReactNode => {
    const col = buildDomainDataColumns({ kind: "admin", ownerId: "u1" }, ctx).find((c) => c.key === "name");
    const r = (col as { render: (v: unknown, rec: Domain, i: number) => ReactNode }).render;
    return r("", record, 0);
  };
  const base = { name: "site.tld", doc_root: "/home/u/domains/site.tld/public_html", email_enabled: true } as Domain;

  it("shows the document root when the domain has no aliases", () => {
    render(<>{nameCell(base)}</>);
    expect(screen.getByText("domains/site.tld/public_html")).toBeTruthy();
  });

  it("shows two aliases and a count instead of the document root, marking an unverified one", () => {
    const { container } = render(
      <>
        {nameCell({
          ...base,
          aliases: [
            { id: "a1", hostname: "site.net", ownership_status: "verified" },
            { id: "a2", hostname: "shop.brandy.io", ownership_status: "pending" },
            { id: "a3", hostname: "site.org", ownership_status: "verified" },
          ],
        })}
      </>,
    );
    const line = container.querySelector("[title^='Aliases:']");
    expect(line?.textContent).toBe("site.net, shop.brandy.io (unverified) +1 more");
    expect(screen.queryByText("domains/site.tld/public_html")).toBeNull();
    const title = line?.getAttribute("title") ?? "";
    expect(title).toContain("site.org");
    expect(title).toContain("Document root: domains/site.tld/public_html");
  });
});

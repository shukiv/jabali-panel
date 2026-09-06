// domainColumns.test — GH #1543 (D1). The Application column is tenant-only:
// the tenant Web Domains list gains it; the admin list (which edits from its
// own Edit page and has a dedicated Applications list) must not.
import { describe, it, expect, vi } from "vitest";
import { buildDomainDataColumns, type DomainInventoryAudience } from "./domainColumns";

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

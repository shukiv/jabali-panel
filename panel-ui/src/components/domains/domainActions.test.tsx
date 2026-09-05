// JAB-300: the row-action menu builder is the single source for both the admin
// and tenant domain lists — the place the two hand-kept item lists used to
// drift. These tests pin each audience's item set, the capability gates, the
// System-domain delete guard (applied universally now), and that every item
// routes to the callback DomainInventory supplies.
import { describe, it, expect, vi } from "vitest";
import { buildDomainMenuItems, type DomainMenuCtx } from "./domainActions";
import type { Domain } from "./types";

const row = (over: Partial<Domain> = {}): Domain => ({
  id: "d1",
  user_id: "u1",
  name: "site.tld",
  doc_root: "/home/alice/site.tld",
  is_enabled: true,
  nginx_custom_directives: "",
  created_at: "",
  updated_at: "",
  ...over,
});

const ctx = (over: Partial<DomainMenuCtx> = {}): DomainMenuCtx => ({
  audience: { kind: "tenant" },
  caps: {},
  togglingId: null,
  navigate: vi.fn(),
  onOpenModal: vi.fn(),
  onToggle: vi.fn(),
  onTogglePreview: vi.fn(),
  onToggleBot: vi.fn(),
  onDelete: vi.fn(),
  ...over,
});

type Item = { key?: string; type?: string; label?: unknown; danger?: boolean; disabled?: boolean; onClick?: () => void };
const asItems = (items: ReturnType<typeof buildDomainMenuItems>): Item[] =>
  ((items ?? []) as unknown as Item[]).filter((i): i is Item => !!i);
const keys = (items: ReturnType<typeof buildDomainMenuItems>): string[] =>
  asItems(items).filter((i) => i.type !== "divider").map((i) => i.key!);
const byKey = (items: ReturnType<typeof buildDomainMenuItems>, key: string): Item | undefined =>
  asItems(items).find((i) => i.key === key);

describe("buildDomainMenuItems — admin audience", () => {
  const admin = { kind: "admin" as const };

  it("offers the admin item set and none of the tenant-only actions", () => {
    const k = keys(buildDomainMenuItems(row(), ctx({ audience: admin, caps: { dns_enabled: true } })));
    expect(k).toEqual(["edit", "dns", "info", "redirects", "index", "settings", "caching", "toggle", "delete"]);
    // tenant-only actions never appear on the admin list
    for (const t of ["directory-privacy", "nginx-options", "rewrite-rules", "document-root", "preview-url", "bot-challenge"]) {
      expect(k).not.toContain(t);
    }
  });

  it("hides DNS when the DNS module is off (GH #1419)", () => {
    const k = keys(buildDomainMenuItems(row(), ctx({ audience: admin, caps: { dns_enabled: false } })));
    expect(k).not.toContain("dns");
    expect(k).toContain("edit");
  });

  it("hides Delete for the System domain (is_panel_primary guard)", () => {
    const k = keys(buildDomainMenuItems(row({ is_panel_primary: true }), ctx({ audience: admin })));
    expect(k).not.toContain("delete");
  });

  it("Edit and DNS navigate to the admin route prefix", () => {
    const navigate = vi.fn();
    const items = buildDomainMenuItems(row(), ctx({ audience: admin, caps: { dns_enabled: true }, navigate }));
    byKey(items, "edit")?.onClick?.();
    expect(navigate).toHaveBeenCalledWith("/jabali-admin/domains/edit/d1");
    byKey(items, "dns")?.onClick?.();
    expect(navigate).toHaveBeenCalledWith("/jabali-admin/domains/d1/dns");
  });
});

describe("buildDomainMenuItems — tenant audience", () => {
  it("offers the tenant base set and none of the admin-only actions", () => {
    const k = keys(buildDomainMenuItems(row(), ctx({ caps: { dns_enabled: true } })));
    expect(k).toEqual([
      "dns", "redirects", "index", "directory-privacy", "caching", "preview-url", "bot-challenge", "toggle", "delete",
    ]);
    for (const a of ["edit", "info", "settings"]) expect(k).not.toContain(a);
  });

  it("adds the nginx option actions only when tenant_domain_options_enabled", () => {
    expect(keys(buildDomainMenuItems(row(), ctx()))).not.toContain("nginx-options");
    const k = keys(buildDomainMenuItems(row(), ctx({ caps: { tenant_domain_options_enabled: true } })));
    expect(k).toContain("nginx-options");
    expect(k).toContain("rewrite-rules");
  });

  it("adds the document-root action only when tenant_docroot_editable", () => {
    expect(keys(buildDomainMenuItems(row(), ctx()))).not.toContain("document-root");
    expect(keys(buildDomainMenuItems(row(), ctx({ caps: { tenant_docroot_editable: true } })))).toContain("document-root");
  });

  it("DNS navigates to the tenant route prefix", () => {
    const navigate = vi.fn();
    const items = buildDomainMenuItems(row(), ctx({ caps: { dns_enabled: true }, navigate }));
    byKey(items, "dns")?.onClick?.();
    expect(navigate).toHaveBeenCalledWith("/jabali-panel/domains/d1/dns");
  });
});

describe("buildDomainMenuItems — shared lifecycle wiring", () => {
  it("routes toggle / delete / preview / bot to the supplied callbacks", () => {
    const c = ctx();
    const r = row();
    const items = buildDomainMenuItems(r, c);
    byKey(items, "toggle")?.onClick?.();
    byKey(items, "delete")?.onClick?.();
    byKey(items, "preview-url")?.onClick?.();
    byKey(items, "bot-challenge")?.onClick?.();
    expect(c.onToggle).toHaveBeenCalledWith(r);
    expect(c.onDelete).toHaveBeenCalledWith(r);
    expect(c.onTogglePreview).toHaveBeenCalledWith(r);
    expect(c.onToggleBot).toHaveBeenCalledWith(r);
  });

  it("routes every modal-opening item to onOpenModal with this row's id (AC6)", () => {
    // Admin modal items.
    const onOpenAdmin = vi.fn();
    const admin = buildDomainMenuItems(
      row({ id: "d2" }),
      ctx({ audience: { kind: "admin" }, onOpenModal: onOpenAdmin }),
    );
    for (const k of ["info", "redirects", "index", "settings", "caching"]) byKey(admin, k)?.onClick?.();
    expect(onOpenAdmin.mock.calls).toEqual([
      ["d2", "info"], ["d2", "redirects"], ["d2", "index"], ["d2", "settings"], ["d2", "caching"],
    ]);

    // Tenant modal items (with the option + docroot caps on so all appear).
    const onOpenTenant = vi.fn();
    const tenant = buildDomainMenuItems(
      row({ id: "d2" }),
      ctx({ caps: { tenant_domain_options_enabled: true, tenant_docroot_editable: true }, onOpenModal: onOpenTenant }),
    );
    for (const k of ["redirects", "index", "directory-privacy", "caching", "nginx-options", "rewrite-rules", "document-root"]) {
      byKey(tenant, k)?.onClick?.();
    }
    expect(onOpenTenant.mock.calls).toEqual([
      ["d2", "redirects"], ["d2", "index"], ["d2", "directory-privacy"], ["d2", "caching"],
      ["d2", "nginx-options"], ["d2", "rewrite-rules"], ["d2", "document-root"],
    ]);
  });

  it("labels the toggle by enabled state and disables it while toggling", () => {
    const enabled = byKey(buildDomainMenuItems(row({ is_enabled: true }), ctx()), "toggle");
    expect(enabled?.label).toBe("Disable");
    expect(enabled?.danger).toBe(true);
    const disabled = byKey(buildDomainMenuItems(row({ is_enabled: false }), ctx()), "toggle");
    expect(disabled?.label).toBe("Enable");
    const toggling = byKey(buildDomainMenuItems(row({ id: "d1" }), ctx({ togglingId: "d1" })), "toggle");
    expect(toggling?.disabled).toBe(true);
  });
});

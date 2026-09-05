// JAB-300: the row-action menu builder is the single source for both the admin
// and tenant domain lists — the place the two hand-kept item lists used to
// drift. These tests pin each audience's item set, the capability gates, the
// System-domain delete guard (applied universally now), and that every item
// routes to the callback DomainInventory supplies.
//
// GH #1543: the tenant per-domain editors moved onto the dedicated Web Domain
// page, so the tenant menu is now just DNS + Enable/Disable + Delete. The admin
// list keeps its full modal-driven menu.
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
    expect(k).toEqual(["edit", "dns", "info", "redirects", "index", "settings", "caching", "chown", "toggle", "delete"]);
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
  it("is stripped to Enable/Delete — every per-domain surface (incl. DNS) moved to the Web Domain page (GH #1543)", () => {
    const k = keys(buildDomainMenuItems(row(), ctx({ caps: { dns_enabled: true } })));
    expect(k).toEqual(["toggle", "delete"]);
    // Everything that used to open a row modal, toggle from the row, or navigate
    // to DNS now lives on the page reached by clicking the domain name.
    for (const gone of [
      "edit", "info", "settings",
      "dns", "redirects", "index", "directory-privacy", "caching",
      "nginx-options", "rewrite-rules", "document-root",
      "preview-url", "bot-challenge",
    ]) {
      expect(k).not.toContain(gone);
    }
  });

  it("does not re-add the editor items even when the tenant caps are on (they moved to the page)", () => {
    const k = keys(
      buildDomainMenuItems(
        row(),
        ctx({ caps: { dns_enabled: true, tenant_domain_options_enabled: true, tenant_docroot_editable: true } }),
      ),
    );
    expect(k).toEqual(["toggle", "delete"]);
  });
});

describe("buildDomainMenuItems — shared lifecycle wiring", () => {
  it("routes toggle / delete to the supplied callbacks", () => {
    const c = ctx();
    const r = row();
    const items = buildDomainMenuItems(r, c);
    byKey(items, "toggle")?.onClick?.();
    byKey(items, "delete")?.onClick?.();
    expect(c.onToggle).toHaveBeenCalledWith(r);
    expect(c.onDelete).toHaveBeenCalledWith(r);
  });

  it("routes the admin modal items to onOpenModal; the tenant menu opens none (GH #1543)", () => {
    // Admin still drives its per-domain editors through row modals.
    const onOpenAdmin = vi.fn();
    const admin = buildDomainMenuItems(
      row({ id: "d2" }),
      ctx({ audience: { kind: "admin" }, onOpenModal: onOpenAdmin }),
    );
    for (const k of ["info", "redirects", "index", "settings", "caching"]) byKey(admin, k)?.onClick?.();
    expect(onOpenAdmin.mock.calls).toEqual([
      ["d2", "info"], ["d2", "redirects"], ["d2", "index"], ["d2", "settings"], ["d2", "caching"],
    ]);

    // The tenant menu no longer opens any modal — clicking every item it offers
    // (toggle / delete) never calls onOpenModal.
    const onOpenTenant = vi.fn();
    const tenant = buildDomainMenuItems(
      row({ id: "d2" }),
      ctx({ caps: { dns_enabled: true, tenant_domain_options_enabled: true, tenant_docroot_editable: true }, onOpenModal: onOpenTenant }),
    );
    for (const i of asItems(tenant)) i.onClick?.();
    expect(onOpenTenant).not.toHaveBeenCalled();
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

import { describe, expect, it } from "vitest";
import {
  adminNav,
  navGroupForKey,
  selectedNavKey,
  userNav,
  userNavGroups,
} from "./nav";

describe("adminNav", () => {
  it("puts Support last", () => {
    expect(adminNav[adminNav.length - 1].key).toBe("support");
  });

  it("gives every sidebar item a hover description", () => {
    const missing = adminNav.filter((n) => !n.description || n.description.trim() === "");
    expect(missing.map((n) => n.key)).toEqual([]);
  });

  // JAB-126: Sessions moved into the Users page as a tab.
  it("has no standalone Sessions sidebar entry", () => {
    expect(adminNav.some((n) => n.key === "sessions")).toBe(false);
    expect(adminNav.some((n) => n.key === "users")).toBe(true);
  });

  it("keeps Users highlighted while viewing the Sessions tab", () => {
    // ?tab=sessions lives on the /jabali-admin/users path, so the sidebar
    // resolves to the Users row (selectedNavKey ignores the query string).
    expect(selectedNavKey(adminNav, "/jabali-admin/users")).toBe("users");
  });
});

// GH #1626: the tenant sidebar groups userNav leaves into Hosting /
// Services / Tools / Account. These invariants keep the grouping honest —
// a new userNav entry that isn't slotted into a group (or is slotted into
// two) fails here rather than silently vanishing from the sidebar.
describe("userNavGroups", () => {
  const groupedKeys = userNavGroups.flatMap((g) => g.itemKeys);
  const navKeys = new Set(userNav.map((n) => n.key));

  it("references only real userNav keys", () => {
    const unknown = groupedKeys.filter((k) => !navKeys.has(k));
    expect(unknown).toEqual([]);
  });

  it("covers every userNav key exactly once, plus standalone Dashboard", () => {
    // Dashboard is the one standalone entry (no group).
    expect(groupedKeys).not.toContain("dashboard");
    const covered = new Set([...groupedKeys, "dashboard"]);
    expect([...navKeys].filter((k) => !covered.has(k))).toEqual([]);
    // No leaf lands in two groups.
    expect(groupedKeys.length).toBe(new Set(groupedKeys).size);
    // Union (minus dashboard) is exactly userNav.
    expect(new Set(groupedKeys).size).toBe(navKeys.size - 1);
  });

  it("gives every collapsible group an icon, and non-collapsible groups none", () => {
    for (const g of userNavGroups) {
      if (g.collapsible) expect(g.icon).toBeTruthy();
      else expect(g.icon).toBeUndefined();
    }
  });

  it("maps a leaf back to its owning group", () => {
    // Pins that navGroupForKey is a real lookup, not a literal someone can
    // let drift: Files resolves to Tools, and its route still highlights it.
    expect(selectedNavKey(userNav, "/jabali-panel/files")).toBe("files");
    expect(navGroupForKey("files")?.key).toBe("tools");
    expect(navGroupForKey("domains")?.key).toBe("hosting");
    // Dashboard has no group.
    expect(navGroupForKey("dashboard")).toBeUndefined();
  });
});

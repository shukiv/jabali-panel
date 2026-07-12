import { describe, expect, it } from "vitest";
import { adminNav, selectedNavKey } from "./nav";

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

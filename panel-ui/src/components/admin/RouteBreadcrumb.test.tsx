import { describe, expect, it } from "vitest";

import type { NavItem } from "../../nav";
import { deriveCrumbs } from "./RouteBreadcrumb";

const nav: NavItem[] = [
  { key: "dashboard", label: "Dashboard", path: "/jabali-admin/dashboard" },
  { key: "users", label: "Users", path: "/jabali-admin/users" },
  { key: "domains", label: "Domains", path: "/jabali-admin/domains" },
] as NavItem[];

const HOME = "/jabali-admin/dashboard";

function titles(path: string) {
  return deriveCrumbs(path, nav, HOME, "Dashboard").map((c) => c.title);
}

describe("deriveCrumbs", () => {
  it("dashboard = single unlinked Home crumb", () => {
    const crumbs = deriveCrumbs(HOME, nav, HOME, "Dashboard");
    expect(crumbs).toHaveLength(1);
    expect(crumbs[0]).toEqual({ title: "Dashboard" });
  });

  it("section list page = Home > Section (section unlinked as current)", () => {
    const crumbs = deriveCrumbs("/jabali-admin/users", nav, HOME, "Dashboard");
    expect(crumbs.map((c) => c.title)).toEqual(["Dashboard", "Users"]);
    expect(crumbs[0].href).toBe(HOME); // home linked
    expect(crumbs[1].href).toBeUndefined(); // current page, not linked
  });

  it("maps known trailing segments and skips opaque ids", () => {
    // /users/<ULID>/edit  → Dashboard > Users > Edit  (id dropped)
    expect(titles("/jabali-admin/users/01ARZ3NDEKTSV4RRFFQ69G5FAV/edit")).toEqual([
      "Dashboard",
      "Users",
      "Edit",
    ]);
  });

  it("title-cases unknown segments", () => {
    expect(titles("/jabali-admin/domains/dns-records")).toEqual(["Dashboard", "Domains", "Dns records"]);
  });

  it("falls back to Home when no section matches", () => {
    expect(titles("/jabali-admin/unmapped")).toEqual(["Dashboard"]);
  });
});

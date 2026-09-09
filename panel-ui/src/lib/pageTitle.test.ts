import { describe, expect, it } from "vitest";

import { buildPageTitle } from "./pageTitle";

describe("buildPageTitle (GH #1604)", () => {
  it("prefixes the host before the product name", () => {
    expect(buildPageTitle("panel.example.com")).toBe("panel.example.com | Jabali Panel");
  });

  it("passes a bare IP through unchanged", () => {
    expect(buildPageTitle("192.0.2.10")).toBe("192.0.2.10 | Jabali Panel");
  });

  it("trims surrounding whitespace", () => {
    expect(buildPageTitle("  host.local  ")).toBe("host.local | Jabali Panel");
  });

  it("falls back to the bare product name for an empty host", () => {
    expect(buildPageTitle("")).toBe("Jabali Panel");
    expect(buildPageTitle("   ")).toBe("Jabali Panel");
  });

  it("falls back for null/undefined", () => {
    expect(buildPageTitle(null)).toBe("Jabali Panel");
    expect(buildPageTitle(undefined)).toBe("Jabali Panel");
  });
});

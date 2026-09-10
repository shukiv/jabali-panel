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

  it("composes a custom brand as the suffix, keeping the host", () => {
    expect(buildPageTitle("panel.example.com", "Acme")).toBe(
      "panel.example.com | Acme — Panel",
    );
  });

  it("uses the default product name when the brand is empty/whitespace", () => {
    expect(buildPageTitle("host.local", "")).toBe("host.local | Jabali Panel");
    expect(buildPageTitle("host.local", "   ")).toBe("host.local | Jabali Panel");
    expect(buildPageTitle("host.local", null)).toBe("host.local | Jabali Panel");
  });

  it("drops the host prefix but keeps the brand when host is empty", () => {
    expect(buildPageTitle("", "Acme")).toBe("Acme — Panel");
  });

  it("trims a custom brand", () => {
    expect(buildPageTitle("host.local", "  Acme  ")).toBe("host.local | Acme — Panel");
  });
});

// grantLabel.test.ts — GH #1415. The Databases page rendered one Tag per
// grant, labelled by grantLabel(). The API serializes a grant's privileges
// as a JSON array (Go []string), but the frontend Grant type declared it a
// `string` and the "custom" branch called `.privileges.split(",")`. On any
// grant saved with a custom privilege set (grant_level "custom" — e.g. a
// tenant ticking individual permission boxes), split-on-an-array threw a
// TypeError mid-render, which the error boundary caught as
// "Something went wrong", taking the whole Databases page down.
//
// These lock the label logic against the ACTUAL wire shape (array).
import { describe, expect, it } from "vitest";

import { grantLabel, type Grant } from "./grantLabel";

function grant(over: Partial<Grant>): Grant {
  return {
    id: "g1",
    database_id: "d1",
    database_name: "db1",
    grant_level: "rw",
    ...over,
  };
}

describe("grantLabel", () => {
  it("labels full access", () => {
    expect(grantLabel(grant({ grant_level: "rw" }))).toBe("Full Access");
  });

  it("labels read only", () => {
    expect(grantLabel(grant({ grant_level: "ro" }))).toBe("Read only");
  });

  it("renders a custom grant whose privileges arrive as an array (GH #1415)", () => {
    // The exact shape the API sends for a custom set — an array, not a
    // comma-joined string. Must not throw.
    const g = grant({
      grant_level: "custom",
      privileges: ["SELECT", "INSERT", "UPDATE", "DELETE"],
    });
    expect(() => grantLabel(g)).not.toThrow();
    expect(grantLabel(g)).toBe("SELECT, INSERT…");
  });

  it("joins a short custom set without truncation", () => {
    expect(
      grantLabel(grant({ grant_level: "custom", privileges: ["SELECT", "INSERT"] })),
    ).toBe("SELECT, INSERT");
  });

  it("falls back to 'Custom' when a custom grant has no privileges", () => {
    expect(grantLabel(grant({ grant_level: "custom", privileges: [] }))).toBe("Custom");
    expect(grantLabel(grant({ grant_level: "custom" }))).toBe("Custom");
  });
});

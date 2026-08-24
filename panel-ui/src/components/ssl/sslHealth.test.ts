import { describe, expect, it } from "vitest";

import {
  certBucket,
  countBuckets,
  daysUntil,
  expiryFraction,
  matchesFilter,
  modeTag,
} from "./sslHealth";

const inDays = (n: number) => new Date(Date.now() + n * 24 * 3600 * 1000).toISOString();

describe("certBucket", () => {
  it("issued with a comfortable expiry stays issued", () => {
    expect(certBucket({ status: "issued", expires_at: inDays(74) })).toBe("issued");
  });

  it("issued at ≤30 days (and expired) is expiring — disjoint from issued", () => {
    expect(certBucket({ status: "issued", expires_at: inDays(12) })).toBe("expiring");
    expect(certBucket({ status: "issued", expires_at: inDays(-2) })).toBe("expiring");
  });

  it("failed and pending_acme_retry share the failed bucket", () => {
    expect(certBucket({ status: "failed", expires_at: null })).toBe("failed");
    expect(certBucket({ status: "pending_acme_retry", expires_at: null })).toBe("failed");
  });

  it("self_signed and lifecycle states bucket separately", () => {
    expect(certBucket({ status: "self_signed", expires_at: null })).toBe("self_signed");
    expect(certBucket({ status: "issuing", expires_at: null })).toBe("other");
  });
});

describe("matchesFilter / countBuckets", () => {
  const rows = [
    { status: "issued", expires_at: inDays(74) },
    { status: "issued", expires_at: inDays(5) },
    { status: "failed", expires_at: null },
    { status: "self_signed", expires_at: null },
  ];

  it("'all' matches everything; buckets are exclusive", () => {
    expect(rows.filter((r) => matchesFilter(r, "all"))).toHaveLength(4);
    expect(rows.filter((r) => matchesFilter(r, "expiring"))).toHaveLength(1);
    const counts = countBuckets(rows);
    expect(counts).toEqual({ issued: 1, expiring: 1, failed: 1, self_signed: 1, other: 0 });
    expect(Object.values(counts).reduce((a, b) => a + b, 0)).toBe(rows.length);
  });
});

describe("expiry math", () => {
  it("daysUntil handles null and junk", () => {
    expect(daysUntil(null)).toBeNull();
    expect(daysUntil("not-a-date")).toBeNull();
  });

  it("expiryFraction clamps to [0,1] over a 90-day lifetime", () => {
    expect(expiryFraction(inDays(45))).toBeCloseTo(0.5, 1);
    expect(expiryFraction(inDays(400))).toBe(1);
    expect(expiryFraction(inDays(-5))).toBe(0);
    expect(expiryFraction(null)).toBeNull();
  });
});

describe("modeTag", () => {
  it("maps the domain ssl_mode enum", () => {
    expect(modeTag("le")).toEqual({ color: "blue", label: "LE" });
    expect(modeTag("shared")).toEqual({ color: "purple", label: "shared" });
    expect(modeTag("custom")).toEqual({ color: "gold", label: "custom" });
    expect(modeTag("self")).toEqual({ label: "self" });
    expect(modeTag(undefined)).toBeNull();
    expect(modeTag("")).toBeNull();
  });
});

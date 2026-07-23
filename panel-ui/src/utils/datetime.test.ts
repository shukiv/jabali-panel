import { describe, expect, it } from "vitest";

import { shortDateTime, shortDateTimeUTC } from "./datetime";

describe("shortDateTimeUTC", () => {
  // GH #454: a backup schedule runs on a UTC cron; the tenant's next-run must
  // render in UTC (03:00) to match the admin's `0 3 * * *`, regardless of the
  // viewer's browser timezone.
  it("renders a UTC instant in UTC with a UTC suffix", () => {
    expect(shortDateTimeUTC("2026-07-24T03:00:00Z")).toBe("24 Jul 2026, 03:00 UTC");
  });

  it("renders an offset instant back in UTC (not local)", () => {
    // 04:00+01:00 == 03:00 UTC
    expect(shortDateTimeUTC("2026-07-24T04:00:00+01:00")).toBe("24 Jul 2026, 03:00 UTC");
  });

  it("returns an em dash for empty/invalid input", () => {
    expect(shortDateTimeUTC(null)).toBe("—");
    expect(shortDateTimeUTC(undefined)).toBe("—");
    expect(shortDateTimeUTC("not-a-date")).toBe("—");
  });

  it("shortDateTime still exists and formats (local, no suffix)", () => {
    expect(shortDateTime(null)).toBe("—");
    // A UTC-midnight value formats without a 'UTC' suffix (local formatter).
    expect(shortDateTimeUTC("2026-07-24T03:00:00Z")).toContain("UTC");
    expect(shortDateTime("2026-07-24T03:00:00Z")).not.toContain("UTC");
  });
});

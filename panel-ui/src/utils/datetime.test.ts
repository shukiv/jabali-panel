import { afterEach, describe, expect, it } from "vitest";

import { DATE_FORMAT_PRESETS } from "./dateFormats";
import {
  formatPattern,
  getPanelClock,
  setPanelClock,
  shortDate,
  shortDateTime,
  shortDateTimeSeconds,
  shortTime,
  subscribePanelClock,
  utcISO,
} from "./datetime";

// GH #2164: every time shows in the Server Settings timezone with the admin's
// date format, whatever the viewer's browser zone is.
afterEach(() => setPanelClock("UTC", undefined));

describe("panel clock", () => {
  it("defaults to UTC and DD/MM/YY HH:mm before /me answers", () => {
    setPanelClock(undefined, undefined);
    expect(getPanelClock().timezone).toBe("UTC");
    expect(shortDateTime("2026-10-16T06:36:12Z")).toBe("16/10/26 06:36");
  });

  it("shows an instant in the server zone, not the browser's", () => {
    setPanelClock("Asia/Jerusalem", "DD/MM/YY HH:mm");
    // 03:36Z is 06:36 in Jerusalem (UTC+3 in October).
    expect(shortDateTime("2026-10-16T03:36:12Z")).toBe("16/10/26 06:36");
    expect(shortDateTimeSeconds("2026-10-16T03:36:12Z")).toBe("16/10/26 06:36:12");
    expect(shortTime("2026-10-16T03:36:12Z")).toBe("06:36");
  });

  it("crosses the date line with the zone", () => {
    setPanelClock("America/New_York", "YYYY-MM-DD HH:mm");
    expect(shortDateTime("2026-10-16T02:00:00Z")).toBe("2026-10-15 22:00");
  });

  it("reads an offset or an offset-less string as the same instant", () => {
    setPanelClock("UTC", undefined);
    expect(shortDateTime("2026-10-16T09:36:00+03:00")).toBe("16/10/26 06:36");
    // The API's timestamps are UTC; one without an offset is UTC too.
    expect(shortDateTime("2026-10-16 06:36:00")).toBe("16/10/26 06:36");
  });

  it("keeps a bare calendar day on its day in every zone", () => {
    setPanelClock("America/Los_Angeles", undefined);
    expect(shortDate("2026-12-01")).toBe("01/12/26");
  });

  it("renders every preset with its own seconds and date forms", () => {
    setPanelClock("UTC", "MM/DD/YYYY hh:mm A");
    expect(shortDateTime("2026-10-16T18:05:09Z")).toBe("10/16/2026 06:05 PM");
    expect(shortDateTimeSeconds("2026-10-16T18:05:09Z")).toBe("10/16/2026 06:05:09 PM");
    expect(shortDate("2026-10-16T18:05:09Z")).toBe("10/16/2026");
    setPanelClock("UTC", "DD MMM YYYY, HH:mm");
    expect(shortDateTime("2026-10-16T18:05:09Z")).toBe("16 Oct 2026, 18:05");
    for (const p of DATE_FORMAT_PRESETS) {
      setPanelClock("UTC", p.dateTime);
      expect(shortDateTime("2026-10-16T18:05:09Z")).not.toBe("—");
    }
  });

  it("falls back to the default format and UTC on unknown values", () => {
    setPanelClock("Mars/Olympus", "%Y-%m-%d");
    expect(getPanelClock().timezone).toBe("UTC");
    expect(shortDateTime("2026-10-16T06:36:00Z")).toBe("16/10/26 06:36");
  });

  it("tells subscribers about a change, and only a change", () => {
    let calls = 0;
    const off = subscribePanelClock(() => calls++);
    setPanelClock("Europe/Berlin", "DD.MM.YYYY HH:mm");
    setPanelClock("Europe/Berlin", "DD.MM.YYYY HH:mm");
    off();
    setPanelClock("UTC", undefined);
    expect(calls).toBe(1);
  });

  it("returns an em dash for empty/invalid input", () => {
    for (const v of [null, undefined, "", "not-a-date"]) {
      expect(shortDateTime(v)).toBe("—");
      expect(shortDate(v)).toBe("—");
      expect(utcISO(v)).toBe("");
    }
  });

  it("gives the exact UTC instant for the hover title", () => {
    setPanelClock("Asia/Tokyo", undefined);
    expect(utcISO("2026-10-16T15:36:12.589695+09:00")).toBe("2026-10-16T06:36:12Z");
    expect(utcISO(Date.UTC(2026, 9, 16, 6, 36, 12))).toBe("2026-10-16T06:36:12Z");
  });

  it("isolates a time left-to-right in a right-to-left page", () => {
    // Hebrew/Arabic: without an isolate "16/10/26 06:36" reads "06:36 16/10/26".
    document.documentElement.dir = "rtl";
    try {
      expect(shortDateTime("2026-10-16T06:36:00Z")).toBe("\u206616/10/26 06:36\u2069");
      expect(shortDate(null)).toBe("—");
    } finally {
      document.documentElement.dir = "";
    }
    expect(shortDateTime("2026-10-16T06:36:00Z")).toBe("16/10/26 06:36");
  });

  it("formats an explicit pattern in the panel zone", () => {
    setPanelClock("Asia/Tokyo", undefined);
    expect(formatPattern("2026-10-16T00:00:00Z", "HH:mm")).toBe("09:00");
  });
});

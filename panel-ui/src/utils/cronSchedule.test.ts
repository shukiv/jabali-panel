// cronSchedule.test.ts — pins the shared cron preset source (JAB-298). The
// derived-map parity block is the point: humanizeSchedule and the dropdown
// options can no longer drift because both come from CRON_SCHEDULE_PRESETS.
import { describe, it, expect } from "vitest";
import {
  CRON_ADVANCED,
  CRON_SCHEDULE_PRESETS,
  CRON_SCHEDULE_OPTIONS,
  humanizeSchedule,
} from "./cronSchedule";

describe("humanizeSchedule", () => {
  it("maps every known preset expression to its friendly label", () => {
    expect(humanizeSchedule("* * * * *")).toBe("Every minute");
    expect(humanizeSchedule("0 * * * *")).toBe("Hourly");
    expect(humanizeSchedule("0 3 * * *")).toBe("Daily at 3 AM");
    expect(humanizeSchedule("0 3 * * 0")).toBe("Weekly (Sun 3 AM)");
    expect(humanizeSchedule("0 3 1 * *")).toBe("Monthly (1st 3 AM)");
  });

  it("passes a hand-written (advanced) expression through unchanged", () => {
    expect(humanizeSchedule("*/15 * * * *")).toBe("*/15 * * * *");
    expect(humanizeSchedule("0 0 * * 1-5")).toBe("0 0 * * 1-5");
  });

  it("passes the advanced sentinel and the empty string through unchanged", () => {
    // 'advanced' is a modal sentinel, never a stored schedule — but the list
    // must not invent a label for it if one ever leaks through.
    expect(humanizeSchedule(CRON_ADVANCED)).toBe("advanced");
    expect(humanizeSchedule("")).toBe("");
  });
});

describe("preset sources", () => {
  it("CRON_SCHEDULE_PRESETS holds the five real presets and no sentinel", () => {
    expect(CRON_SCHEDULE_PRESETS).toHaveLength(5);
    expect(CRON_SCHEDULE_PRESETS.some((p) => p.value === CRON_ADVANCED)).toBe(false);
  });

  it("CRON_SCHEDULE_OPTIONS is the presets plus the Advanced sentinel last", () => {
    expect(CRON_SCHEDULE_OPTIONS).toHaveLength(CRON_SCHEDULE_PRESETS.length + 1);
    expect(CRON_SCHEDULE_OPTIONS.slice(0, -1)).toEqual(CRON_SCHEDULE_PRESETS);
    expect(CRON_SCHEDULE_OPTIONS[CRON_SCHEDULE_OPTIONS.length - 1]).toEqual({
      label: "Advanced",
      value: CRON_ADVANCED,
    });
  });

  it("PARITY: humanizeSchedule round-trips every option that isn't the sentinel", () => {
    for (const p of CRON_SCHEDULE_OPTIONS) {
      if (p.value === CRON_ADVANCED) continue;
      expect(humanizeSchedule(p.value)).toBe(p.label);
    }
  });
});

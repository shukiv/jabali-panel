// The admin schedule form reads back every time it writes (GH #2246). A cron
// it can't read opened as "Daily 03:00" and Save replaced it.
import dayjs from "dayjs";
import { describe, expect, it } from "vitest";

import { HOURLY_EVERY, cronFromSpec, specFromCron } from "./scheduleCron";

describe("scheduleCron", () => {
  it.each(HOURLY_EVERY)("hourly every %i round-trips", (every) => {
    const cron = cronFromSpec({ freq: "hourly", time: dayjs().hour(0).minute(15), every });
    expect(cron).toBe(every === 1 ? "15 * * * *" : `15 */${every} * * *`);
    const spec = specFromCron(cron);
    expect(spec?.freq).toBe("hourly");
    expect(spec?.every).toBe(every);
    expect(spec?.time.minute()).toBe(15);
  });

  it.each([
    ["30 2 * * *", "daily"],
    ["0 4 * * 1,5", "weekly"],
    ["45 23 15 * *", "monthly"],
  ])("%s reads as %s and writes back the same", (cron, freq) => {
    const spec = specFromCron(cron);
    expect(spec?.freq).toBe(freq);
    expect(cronFromSpec(spec!)).toBe(cron);
  });

  it.each([
    "15 */5 * * *", // uneven hours
    "*/15 * * * *", // every 15 minutes
    "0 3 * 1 *", // January only
    "0 */6 * * 1", // hourly on Mondays
    "0 3 1 * 1", // day of month and weekday
    "60 3 * * *",
    "0 24 * * *",
    "",
  ])("%j is a time the form can't show", (cron) => {
    expect(specFromCron(cron)).toBeNull();
  });
});

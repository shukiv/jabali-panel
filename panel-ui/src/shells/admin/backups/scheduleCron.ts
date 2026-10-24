// The admin schedule form's timing <-> the 5-field cron the API stores.
import dayjs, { type Dayjs } from "dayjs";

export type Freq = "hourly" | "daily" | "weekly" | "monthly";

// Hours an hourly schedule can repeat every: the divisors of 24, so runs are
// evenly spaced through the day (*/5 would run at 0,5,10,15,20 and then 0).
export const HOURLY_EVERY = [1, 2, 3, 4, 6, 8, 12];

export interface ScheduleSpec {
  freq: Freq;
  time: Dayjs;          // hour:minute; hourly uses the minute only
  every?: number;       // hourly only — hours between runs, from HOURLY_EVERY
  weekdays?: number[];  // weekly only — 0=Sun..6=Sat (cron numbering)
  dom?: number;         // monthly only — 1..31
}

// cronFromSpec converts the form values into a 5-field cron expression.
// Monthly is day-of-month only (Option A); nth-weekday lands when we
// switch off robfig/cron, which OR-merges dom/dow restrictions.
export const cronFromSpec = (s: ScheduleSpec): string => {
  const m = s.time.minute();
  const h = s.time.hour();
  switch (s.freq) {
    case "hourly": {
      const every = s.every ?? 1;
      return `${m} ${every === 1 ? "*" : `*/${every}`} * * *`;
    }
    case "daily":
      return `${m} ${h} * * *`;
    case "weekly": {
      const days = (s.weekdays ?? []).slice().sort((a, b) => a - b);
      const dow = days.length === 0 || days.length === 7 ? "*" : days.join(",");
      return `${m} ${h} * * ${dow}`;
    }
    case "monthly":
      return `${m} ${h} ${s.dom ?? 1} * *`;
  }
};

// specFromCron is best-effort — recognises the shapes cronFromSpec
// emits. Anything else returns null; the drawer then says it can't show
// that time and that saving replaces it.
export const specFromCron = (expr: string): ScheduleSpec | null => {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return null;
  const [mm, hh, dom, mon, dow] = parts;
  const m = Number(mm);
  if (!/^\d+$/.test(mm) || m > 59) return null;
  if (mon !== "*") return null;
  // hourly: hour * or */n, everything else *
  const step = hh === "*" ? "1" : /^\*\/(\d+)$/.exec(hh)?.[1];
  if (step !== undefined) {
    const every = Number(step);
    if (dom !== "*" || dow !== "*" || !HOURLY_EVERY.includes(every)) return null;
    return { freq: "hourly", time: dayjs().hour(0).minute(m).second(0), every };
  }
  const h = Number(hh);
  if (!/^\d+$/.test(hh) || h > 23) return null;
  const time = dayjs().hour(h).minute(m).second(0);
  // daily: dom * dow *
  if (dom === "*" && dow === "*") {
    return { freq: "daily", time };
  }
  // weekly: dom * dow restricted
  if (dom === "*") {
    const days = dow.split(",").map((x) => Number(x)).filter((n) => Number.isInteger(n));
    if (days.length === 0 || days.some((d) => d < 0 || d > 6)) return null;
    return { freq: "weekly", time, weekdays: days };
  }
  // monthly: dom restricted, dow *
  if (dow === "*") {
    const d = Number(dom);
    if (!Number.isInteger(d) || d < 1 || d > 31) return null;
    return { freq: "monthly", time, dom: d };
  }
  return null;
};

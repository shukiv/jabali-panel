// datetime — the panel's one way to show a time (GH #2164). Every timestamp the
// API sends is UTC; the panel shows it in the Server Settings timezone with the
// date format the admin picked there ("16/10/26 06:36" by default), for admins
// and tenants alike. GET /me carries both; AuthContext hands them to
// setPanelClock before any protected page renders. Invalid/empty input renders
// an em dash. <Timestamp> (components/Timestamp.tsx) wraps these with the exact
// UTC time on hover.
import dayjs from "dayjs";
import timezone from "dayjs/plugin/timezone";
import utc from "dayjs/plugin/utc";

import { DEFAULT_DATE_FORMAT, datePreset, type DateFormatPreset } from "./dateFormats";

dayjs.extend(utc);
dayjs.extend(timezone);

const DASH = "—";

/** Anything the API or a component holds as a point in time. */
export type Stamp = string | number | Date | null | undefined;

export interface PanelClock {
  /** IANA zone the panel shows times in. */
  timezone: string;
  /** The admin's date format, with its seconds / date-only / time forms. */
  preset: DateFormatPreset;
}

let clock: PanelClock = { timezone: "UTC", preset: datePreset(DEFAULT_DATE_FORMAT) };
const listeners = new Set<() => void>();

// knownZone reports whether the browser can render in tz. dayjs.tz throws a
// RangeError on a zone the browser's Intl lacks, which would blank every
// timestamp in the panel; such a zone falls back to UTC instead.
function knownZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

// setPanelClock adopts the zone and date format from GET /me.
export function setPanelClock(tz: string | null | undefined, dateFormat: string | null | undefined): void {
  const zone = tz && knownZone(tz) ? tz : "UTC";
  const preset = datePreset(dateFormat);
  if (zone === clock.timezone && preset === clock.preset) return;
  clock = { timezone: zone, preset };
  listeners.forEach((l) => l());
}

export function getPanelClock(): PanelClock {
  return clock;
}

// subscribePanelClock is the useSyncExternalStore subscriber: components that
// render a time re-render when the admin changes the zone or the format.
export function subscribePanelClock(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// parse reads a stamp as an instant. A string without an offset is UTC, like
// every timestamp the API sends.
function parse(ts: Stamp): dayjs.Dayjs | null {
  if (ts === null || ts === undefined || ts === "") return null;
  const d = dayjs.utc(ts);
  return d.isValid() ? d : null;
}

// A bare "YYYY-MM-DD" is a calendar day (an expiry date, a billing day), not an
// instant: it shows as that day in every zone instead of moving to the day
// before west of UTC.
const CALENDAR_DAY = /^\d{4}-\d{2}-\d{2}$/;

function inPanelZone(ts: Stamp): dayjs.Dayjs | null {
  const d = parse(ts);
  if (!d) return null;
  if (typeof ts === "string" && CALENDAR_DAY.test(ts)) return d;
  try {
    return d.tz(clock.timezone);
  } catch {
    return d;
  }
}

function render(ts: Stamp, pattern: (p: DateFormatPreset) => string): string {
  const d = inPanelZone(ts);
  return d ? d.format(pattern(clock.preset)) : DASH;
}

// shortDateTime: date and time, the admin's format. e.g. "16/10/26 06:36".
export function shortDateTime(ts: Stamp): string {
  return render(ts, (p) => p.dateTime);
}

// shortDateTimeSeconds: with seconds, for logs and the audit log.
// e.g. "16/10/26 06:36:12".
export function shortDateTimeSeconds(ts: Stamp): string {
  return render(ts, (p) => p.dateTimeSeconds);
}

// shortDate: date only. e.g. "16/10/26".
export function shortDate(ts: Stamp): string {
  return render(ts, (p) => p.date);
}

// shortTime: time only. e.g. "06:36".
export function shortTime(ts: Stamp): string {
  return render(ts, (p) => p.time);
}

// shortTimeSeconds: time with seconds. e.g. "06:36:12".
export function shortTimeSeconds(ts: Stamp): string {
  return render(ts, (p) => p.timeSeconds);
}

// utcISO: the exact instant in UTC, ISO 8601 without fractions, for hover
// titles. e.g. "2026-10-16T03:36:12Z". "" for empty/invalid input.
export function utcISO(ts: Stamp): string {
  const d = parse(ts);
  return d ? d.utc().format("YYYY-MM-DDTHH:mm:ss[Z]") : "";
}

// formatPattern renders a stamp in the panel zone with an explicit dayjs
// pattern, for the few places that need a fixed shape (chart axes, file
// names). Prefer the short* helpers for anything a person reads in a table.
export function formatPattern(ts: Stamp, pattern: string): string {
  const d = inPanelZone(ts);
  return d ? d.format(pattern) : DASH;
}

// panelNow is the current instant in the panel zone, for previews of the
// date format.
export function panelNow(): dayjs.Dayjs {
  const now = dayjs.utc();
  try {
    return now.tz(clock.timezone);
  } catch {
    return now;
  }
}

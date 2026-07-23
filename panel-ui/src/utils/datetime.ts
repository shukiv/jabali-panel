// datetime — short, human-readable date/time formatters for table cells etc.
// Keeps timestamps compact ("25 Jun 2026, 04:04") instead of raw ISO
// ("2026-06-25T04:04:32.589695Z"). Invalid/empty input renders an em dash.
import dayjs from "dayjs";
import utc from "dayjs/plugin/utc";

dayjs.extend(utc);

const DASH = "—";

// shortDateTime: date + 24h time, no seconds. e.g. "25 Jun 2026, 04:04".
export function shortDateTime(ts: string | null | undefined): string {
  if (!ts) return DASH;
  const d = dayjs(ts);
  return d.isValid() ? d.format("DD MMM YYYY, HH:mm") : DASH;
}

// shortDateTimeUTC: like shortDateTime but rendered in UTC with a "UTC" suffix.
// GH #454: backup schedules run on a UTC cron (the admin edits `0 3 * * *`), so
// the tenant's next-run must show 03:00 UTC to match the admin — not the
// viewer's browser-local tz (which made admin=03:00 read as tenant=04:00).
export function shortDateTimeUTC(ts: string | null | undefined): string {
  if (!ts) return DASH;
  const d = dayjs.utc(ts);
  return d.isValid() ? d.format("DD MMM YYYY, HH:mm") + " UTC" : DASH;
}

// shortDate: date only. e.g. "25 Jun 2026".
export function shortDate(ts: string | null | undefined): string {
  if (!ts) return DASH;
  const d = dayjs(ts);
  return d.isValid() ? d.format("DD MMM YYYY") : DASH;
}

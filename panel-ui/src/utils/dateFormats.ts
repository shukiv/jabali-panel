// dateFormats — the date formats an admin can pick in Server Settings → Server
// Time (GH #2164). `dateTime` is the stored value and the `date_format` the API
// returns; the other fields are the forms the panel needs next to it. Keep the
// `dateTime` list in step with paneltime.DateFormats
// (internal/paneltime/paneltime.go): a Go test compares the two, in order.

export interface DateFormatPreset {
  /** Date with time: table cells, details. The stored id. */
  dateTime: string;
  /** Date with time and seconds: logs and the audit log. */
  dateTimeSeconds: string;
  /** Date only. */
  date: string;
  /** Time only. */
  time: string;
  /** Time with seconds. */
  timeSeconds: string;
}

export const DEFAULT_DATE_FORMAT = "DD/MM/YY HH:mm";

export const DATE_FORMAT_PRESETS: readonly DateFormatPreset[] = [
  { dateTime: "DD/MM/YY HH:mm", dateTimeSeconds: "DD/MM/YY HH:mm:ss", date: "DD/MM/YY", time: "HH:mm", timeSeconds: "HH:mm:ss" },
  { dateTime: "DD/MM/YYYY HH:mm", dateTimeSeconds: "DD/MM/YYYY HH:mm:ss", date: "DD/MM/YYYY", time: "HH:mm", timeSeconds: "HH:mm:ss" },
  { dateTime: "DD.MM.YYYY HH:mm", dateTimeSeconds: "DD.MM.YYYY HH:mm:ss", date: "DD.MM.YYYY", time: "HH:mm", timeSeconds: "HH:mm:ss" },
  { dateTime: "YYYY-MM-DD HH:mm", dateTimeSeconds: "YYYY-MM-DD HH:mm:ss", date: "YYYY-MM-DD", time: "HH:mm", timeSeconds: "HH:mm:ss" },
  { dateTime: "MM/DD/YYYY hh:mm A", dateTimeSeconds: "MM/DD/YYYY hh:mm:ss A", date: "MM/DD/YYYY", time: "hh:mm A", timeSeconds: "hh:mm:ss A" },
  { dateTime: "DD MMM YYYY, HH:mm", dateTimeSeconds: "DD MMM YYYY, HH:mm:ss", date: "DD MMM YYYY", time: "HH:mm", timeSeconds: "HH:mm:ss" },
];

// datePreset returns the preset for a stored format, or the default preset for
// an unknown or empty one.
export function datePreset(id: string | null | undefined): DateFormatPreset {
  return (
    DATE_FORMAT_PRESETS.find((p) => p.dateTime === id) ??
    DATE_FORMAT_PRESETS.find((p) => p.dateTime === DEFAULT_DATE_FORMAT)!
  );
}

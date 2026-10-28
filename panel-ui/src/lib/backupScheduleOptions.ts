// What a scheduled backup backs up, and how often an account's own schedule
// runs. One list for the tenant Backups page and the admin Schedules tab, so
// both show and offer the same choices (GH #2246). Values are the wire values
// the panel API accepts.

export const BACKUP_CONTENT_OPTIONS = [
  { value: "full", label: "Full account" },
  { value: "files", label: "Files only" },
  { value: "database", label: "Databases only" },
];

export const BACKUP_CADENCE_OPTIONS = [
  { value: "hourly", label: "Hourly" },
  { value: "every_6h", label: "Every 6 hours" },
  { value: "every_12h", label: "Every 12 hours" },
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
];

// How many backups each cadence makes in a week. "Days to keep" keeps every
// backup of the last N days (GH #2300), so this turns days into backups. The
// panel API has the same table (internal/backup CadenceRunsPerWeek); a Go
// test keeps them equal.
export const BACKUP_CADENCE_RUNS_PER_WEEK: Record<string, number> = {
  hourly: 168,
  every_6h: 28,
  every_12h: 14,
  daily: 7,
  weekly: 1,
};

export const cadenceRunsPerWeek = (c: string) =>
  BACKUP_CADENCE_RUNS_PER_WEEK[c] ?? 7;

// The most days to keep whose backups fit in the plan's backup limit, and at
// least 1. The API clamps to the same (MaxKeepWithinDays).
export const maxKeepDays = (maxBackups: number, runsPerWeek: number) =>
  Math.max(
    1,
    Math.floor((maxBackups * 7) / (runsPerWeek > 0 ? runsPerWeek : 7)),
  );

// About how many backups `days` days keep. The plan's limit caps it: at the
// limit, a run removes the schedule's oldest backup first.
export const keptBackups = (
  days: number,
  runsPerWeek: number,
  maxBackups: number,
) => Math.min(maxBackups, Math.max(1, Math.ceil((days * runsPerWeek) / 7)));

export const backupContentLabel = (c: string) =>
  BACKUP_CONTENT_OPTIONS.find((o) => o.value === (c || "full"))?.label ?? c;

export const backupCadenceLabel = (c: string) =>
  BACKUP_CADENCE_OPTIONS.find((o) => o.value === c)?.label ?? c;

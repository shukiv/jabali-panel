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

export const backupContentLabel = (c: string) =>
  BACKUP_CONTENT_OPTIONS.find((o) => o.value === (c || "full"))?.label ?? c;

export const backupCadenceLabel = (c: string) =>
  BACKUP_CADENCE_OPTIONS.find((o) => o.value === c)?.label ?? c;

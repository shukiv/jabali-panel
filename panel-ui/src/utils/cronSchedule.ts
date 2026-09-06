// cronSchedule.ts — one source of truth for the cron schedule presets shown in
// the create modals AND used to humanize a stored schedule in the cron lists
// (JAB-298 "schedule presentation ... one Implementation").
//
// Before this, the same five presets were copy-pasted four times: a
// Record<string,string> map in AdminCronList + UserCronList (for humanizing)
// and an options array in AdminCreateCronModal + CreateCronModal (for the
// dropdown), each free to drift. The map is now derived from the array so a
// preset is defined exactly once.

export interface CronPreset {
  label: string;
  value: string;
}

// CRON_ADVANCED is the create-modal sentinel for "let me type a raw cron
// expression" — it is not a real schedule and never appears in a list.
export const CRON_ADVANCED = "advanced";

// The real schedule presets (no Advanced sentinel).
export const CRON_SCHEDULE_PRESETS: readonly CronPreset[] = [
  { label: "Every minute", value: "* * * * *" },
  { label: "Hourly", value: "0 * * * *" },
  { label: "Daily at 3 AM", value: "0 3 * * *" },
  { label: "Weekly (Sun 3 AM)", value: "0 3 * * 0" },
  { label: "Monthly (1st 3 AM)", value: "0 3 1 * *" },
];

// The create-modal option list = the presets plus the Advanced sentinel.
export const CRON_SCHEDULE_OPTIONS: readonly CronPreset[] = [
  ...CRON_SCHEDULE_PRESETS,
  { label: "Advanced", value: CRON_ADVANCED },
];

// expr → friendly label, derived from the presets so there is one source.
const scheduleLabels: Record<string, string> = Object.fromEntries(
  CRON_SCHEDULE_PRESETS.map((p) => [p.value, p.label]),
);

// humanizeSchedule renders a stored cron expression: a known preset shows its
// friendly label; anything else (a hand-written "advanced" expression) shows
// the raw expression unchanged.
export const humanizeSchedule = (schedule: string): string =>
  scheduleLabels[schedule] ?? schedule;

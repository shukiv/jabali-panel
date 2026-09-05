// Shared Docker-app status vocabulary for the inventory Module and its columns
// (JAB-335). Kept in a plain .ts (no JSX exports) so the column builders file
// can stay component-only for fast refresh.

// Statuses that are mid-transition — the row shows a spinner and the installed
// list keeps polling while any row is in one of these.
export const TRANSITIONAL_STATUSES: ReadonlySet<string> = new Set([
  "pending",
  "installing",
  "updating",
  "rolling_back",
]);

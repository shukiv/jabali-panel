// Timestamp — one point in time as the panel shows it (GH #2164): the Server
// Settings timezone and date format, with the exact UTC time on hover. Use it
// for every timestamp a person reads in a table, a description or a card; use
// the string helpers in utils/datetime only where markup can't go (Select
// labels, CSV, chart axes, other components' string props).
import { useSyncExternalStore } from "react";

import {
  formatStamp,
  getPanelClock,
  subscribePanelClock,
  utcISO,
  type PanelClock,
  type Stamp,
} from "../utils/datetime";

interface TimestampProps {
  value: Stamp;
  /** Add seconds (logs, the audit log). */
  seconds?: boolean;
  /** Date only. */
  dateOnly?: boolean;
}

export function Timestamp({ value, seconds, dateOnly }: TimestampProps) {
  // Re-render when the admin changes the zone or the format.
  useSyncExternalStore(subscribePanelClock, getPanelClock);
  const text = formatStamp(value, dateOnly ? "date" : seconds ? "dateTimeSeconds" : "dateTime");
  const iso = utcISO(value);
  if (!iso) return <>{text}</>;
  // dir="ltr" isolates the time: in a right-to-left page the numbers would
  // otherwise reorder ("06:36 16/10/26").
  return (
    <time dateTime={iso} dir="ltr" title={`${iso} (UTC)`}>
      {text}
    </time>
  );
}

// usePanelClock is the panel's zone and date format, re-rendering on change.
export function usePanelClock(): PanelClock {
  return useSyncExternalStore(subscribePanelClock, getPanelClock);
}

export default Timestamp;

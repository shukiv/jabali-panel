// useTransitionalPoll — the "poll while a row is still settling" rule both
// application lists carried inline (JAB-334 AC1). While any row is transitional
// (pending/installing/cloning/deleting) the list refetches on a fixed cadence;
// once every row lands on ready/failed the timer is torn down.
import { useEffect } from "react";
import { anyTransitional, type ApplicationStatus } from "../../utils/applicationStatus";

// Five-second cadence — what Refine's old refetchInterval returned, kept when
// both lists moved off it.
export const APPLICATION_POLL_MS = 5000;

export function useTransitionalPoll(
  rows: ReadonlyArray<{ status: ApplicationStatus }>,
  refetch: () => void,
): void {
  const hasTransitional = anyTransitional(rows);
  useEffect(() => {
    if (!hasTransitional) return;
    const h = setInterval(() => refetch(), APPLICATION_POLL_MS);
    return () => clearInterval(h);
    // refetch identity is stable (useTableURL's is), so only the
    // transitional/settled edge re-installs the timer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasTransitional]);
}

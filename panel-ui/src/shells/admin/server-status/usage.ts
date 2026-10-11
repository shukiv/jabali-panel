// usage.ts — the meter colour rules Server Status and the admin Dashboard
// share (GH #1249), so one host reads the same colour on both pages.
import type { Partition } from "../../../hooks/useServerStatus";

// usageColor colours a CPU / memory / swap meter.
export const usageColor = (pct: number): string => {
  if (pct >= 90) return "#cf1322";
  if (pct >= 70) return "#fa8c16";
  return "#52c41a";
};

// diskUsePct is df's Use% (GH #2029): used as a share of the space non-root can
// use, rounded up. Blocks ext4 reserves for root count as neither used nor
// free, so used + free is less than the size. The panel's disk alerts use the
// same number (internal/fsusage).
export function diskUsePct(p: Partition): number {
  const usable = p.used_bytes + p.free_bytes;
  if (!usable) return 0;
  return Math.ceil((p.used_bytes * 100) / usable);
}

// diskColor colours a disk meter on the panel's 80/95 disk thresholds.
export function diskColor(pct: number): string {
  if (pct >= 95) return "#cf1322";
  if (pct >= 80) return "#fa8c16";
  return "#52c41a";
}

// sslHealth — pure helpers behind the admin "Certificate console"
// (/jabali-admin/ssl): health buckets for the summary tiles + filters,
// days-left math for the expiry meter, and the Mode-tag mapping.
//
// The four buckets are DISJOINT so the tile counts never double-count a
// cert: "expiring" wins over "issued" for issued certs at ≤30 days
// (expired ones included), failed covers both hard-failed and
// retry-pending, self_signed is its own bucket, everything else
// (pending/issuing/renewing/revoked) is "other".

export interface SSLHealthRow {
  status: string;
  expires_at: string | null;
}

export type SSLBucket = "issued" | "expiring" | "failed" | "self_signed" | "other";

/** Tile/segmented filter values: a bucket, or "all". */
export type SSLFilter = SSLBucket | "all";

export const EXPIRING_SOON_DAYS = 30;

export function daysUntil(expiresAt: string | null): number | null {
  if (!expiresAt) return null;
  const t = new Date(expiresAt).getTime();
  if (Number.isNaN(t)) return null;
  return Math.ceil((t - Date.now()) / (24 * 3600 * 1000));
}

export function certBucket(row: SSLHealthRow): SSLBucket {
  switch (row.status) {
    case "issued": {
      const days = daysUntil(row.expires_at);
      return days !== null && days <= EXPIRING_SOON_DAYS ? "expiring" : "issued";
    }
    case "failed":
    case "pending_acme_retry":
      return "failed";
    case "self_signed":
      return "self_signed";
    default:
      return "other";
  }
}

export function matchesFilter(row: SSLHealthRow, filter: SSLFilter): boolean {
  return filter === "all" || certBucket(row) === filter;
}

export function countBuckets(rows: SSLHealthRow[]): Record<SSLBucket, number> {
  const counts: Record<SSLBucket, number> = {
    issued: 0,
    expiring: 0,
    failed: 0,
    self_signed: 0,
    other: 0,
  };
  for (const row of rows) counts[certBucket(row)] += 1;
  return counts;
}

/**
 * Fraction of a 90-day Let's Encrypt lifetime remaining, for the expiry
 * meter. Clamped to [0, 1]; null when there is no usable expiry.
 */
export function expiryFraction(expiresAt: string | null): number | null {
  const days = daysUntil(expiresAt);
  if (days === null) return null;
  return Math.min(1, Math.max(0, days / 90));
}

/** AntD Tag props for the domain ssl_mode. Empty/unknown → null (no tag). */
export function modeTag(mode: string | undefined): { color?: string; label: string } | null {
  switch (mode) {
    case "le":
      return { color: "blue", label: "LE" };
    case "shared":
      return { color: "purple", label: "shared" };
    case "custom":
      return { color: "gold", label: "custom" };
    case "self":
      return { label: "self" };
    case "none":
      return { label: "none" };
    default:
      return null;
  }
}

// applicationInventory.ts — the canonical Application Inventory row plus the
// login capability, invalidation keys, and error extraction the admin and
// tenant application lists both used to define for themselves (JAB-334).
//
// The status vocabulary (status union, badge meta, transitional rule) lives in
// utils/applicationStatus — this module owns the rest of the shared behavior.
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import type { ApplicationStatus } from "../../utils/applicationStatus";

// Canonical Application Inventory row. The admin (cross-user) list carries the
// owner columns; the tenant list carries the #406 cache flag. Both audience
// fields are optional so one type serves both lists.
export interface ApplicationInstall {
  id: string;
  app_type?: string;
  domain_id: string;
  domain_name: string;
  db_id: string;
  admin_username: string;
  admin_email: string;
  locale: string;
  subdirectory: string;
  status: ApplicationStatus;
  version: string | null;
  last_error: string;
  created_at: string;
  updated_at: string;
  // Admin-only (cross-user list).
  owner_email?: string;
  owner_username?: string;
  // Tenant-only (#406 single-switch cache).
  cache_enabled?: boolean;
}

// The CMS types the admin-login (magic-link SSO) flow can drive. This MUST
// match panel-api ssoAgentCommandFor (magic_link.go) — when a new CMS gains an
// SSO-file handler there, widen this set here so the button can't drift from
// the backend capability.
export const APPLICATION_LOGIN_APP_TYPES: ReadonlySet<string> = new Set<string>([
  "wordpress",
  "drupal",
  "joomla",
]);

// canApplicationLogin — is the admin-login action available for this row? Ready
// plus a CMS the SSO flow knows. app_type defaults to wordpress (the historical
// single-CMS value) when the backend omits it. One login rule for both lists
// (JAB-334 AC3).
export const canApplicationLogin = (
  record: Pick<ApplicationInstall, "status" | "app_type">,
): boolean =>
  record.status === "ready" &&
  APPLICATION_LOGIN_APP_TYPES.has(record.app_type ?? "wordpress");

// openApplicationLogin — mint a magic link, open the admin dashboard in a new
// tab, and toast. Both lists ran this identical flow behind their login button;
// centralizing it keeps the SSO affordance in one place (JAB-334 AC3). `mint`
// is useMagicLink().mint; `fallbackError` is its captured error string.
export async function openApplicationLogin(
  mint: () => Promise<{ url: string }>,
  fallbackError?: string | null,
): Promise<void> {
  try {
    const { url } = await mint();
    window.open(url, "_blank", "noopener,noreferrer");
    feedback.message.success("Admin login link opened");
  } catch {
    feedback.message.error(fallbackError || "Failed to generate admin login link");
  }
}

// Deleting an install also frees its database, so both lists invalidate the
// applications AND databases lists after a delete. One canonical key set
// (JAB-334 AC2).
export const APPLICATION_INVALIDATION_KEYS: ReadonlyArray<readonly [string, string]> = [
  ["list", "applications"],
  ["list", "databases"],
];

// extractApiError — the `detail ?? error ?? message ?? fallback` ladder the
// tenant list used for its richer delete/scan errors. Prefers the server's
// `detail`, then `error`, then the transport message.
export function extractApiError(err: unknown, fallback: string): string {
  const e = err as {
    response?: { data?: { detail?: string; error?: string } };
    message?: string;
  };
  return e?.response?.data?.detail ?? e?.response?.data?.error ?? e?.message ?? fallback;
}

// identity.ts — single source of truth for "who am I", shared across
// authProvider, RoleGate, and any page component that needs to know the
// caller's panel user ID or role.
//
// Source: GET /api/v1/me (NOT Kratos whoami directly). The panel-api's
// RequireKratosSession middleware already validates the ory_kratos_session
// cookie and resolves the Kratos identity → panel user row, so /me
// returns claims.UserID (the panel ULID) and claims.IsAdmin (the panel
// DB is_admin, which is authoritative per ADR-0034, not the advisory
// Kratos trait).
//
// Why not whoami: the Kratos identity UUID and the panel ULID are
// DIFFERENT values. Any code that needs to pass `me.id` to a panel
// endpoint (PATCH /users/:id, GET /users/:id/usage, etc.) needs the
// panel ULID — using the Kratos UUID triggers 403 on RequireOwner for
// every non-admin user.
import { apiClient } from "./apiClient";
import { queryClient } from "./query";
import { setPanelClock } from "./utils/datetime";

export type Identity = {
  id: string;
  email: string;
  isAdmin: boolean;
  username?: string;
  fullName?: string;
  nameFirst?: string;
  nameLast?: string;
  createdAt?: string;
  packageId?: string;
  packageName?: string;
  // server_settings.upload_max_size_mb — file-manager upload ceiling
  // (read-only mirror; admin sets it in Server Settings). #211.
  uploadMaxSizeMb?: number;
};

type MeResponse = {
  id: string;
  email: string;
  is_admin: boolean;
  username?: string;
  full_name?: string;
  name_first?: string;
  name_last?: string;
  created_at?: string;
  package_id?: string;
  package_name?: string;
  upload_max_size_mb?: number;
  /** GH #2164: the panel's clock (Server Settings timezone + date format). */
  timezone?: string;
  date_format?: string;
};

let cached: Identity | null = null;
let inflight: Promise<Identity | null> | null = null;

/**
 * Fetch the current user's identity, memoized across the session.
 *
 * Returns null when we're not logged in (401) or when the identity
 * service is transiently unreachable. The caller (authProvider.check,
 * RoleGate, etc.) sees `null` the same way either way and routes to
 * /login — that's acceptable because a genuinely logged-in user with
 * a transient blip just re-auths once.
 */
export async function getIdentity(): Promise<Identity | null> {
  if (cached) return cached;
  if (inflight) return inflight;

  inflight = (async () => {
    try {
      const { data } = await apiClient.get<MeResponse>("/me");
      if (!data?.id) return null;
      setPanelClock(data.timezone, data.date_format);
      cached = {
        id: data.id,
        email: data.email ?? "",
        isAdmin: data.is_admin === true,
        username: data.username,
        fullName: data.full_name,
        nameFirst: data.name_first,
        nameLast: data.name_last,
        createdAt: data.created_at,
        packageId: data.package_id,
        packageName: data.package_name,
        uploadMaxSizeMb: data.upload_max_size_mb,
      };
      return cached;
    } catch {
      // 401 (no session) and transient 5xx/network both collapse to null
      // here. The Refine authProvider.onError interceptor separately
      // handles 401 routing to /login for the initial call site.
      return null;
    } finally {
      inflight = null;
    }
  })();

  return inflight;
}

/** Drop the cache. Call on logout or on any 401 that bubbles from /me. */
export function clearIdentity(): void {
  cached = null;
  inflight = null;
  // Also invalidate the TanStack whoami cache that AuthContext reads
  // from. Without this, Login.tsx's post-submit navigate to /jabali-*
  // finds AuthProvider still holding the pre-login `null` response
  // (staleTime 60s) and RequireAdmin bounces back to /login — a loop
  // that only breaks when the user refreshes the tab.
  queryClient.invalidateQueries({ queryKey: ["whoami"] });
}

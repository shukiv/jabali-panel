// AuthContext.tsx — single source of identity state for the SPA,
// backed by TanStack Query so every consumer shares the same
// whoami cache. Replaces Refine's <Authenticated> / authProvider
// plumbing one layer at a time.
//
// Identity source: GET /api/v1/me. Same endpoint identity.ts already
// uses — panel-api's RequireKratosSession middleware validates the
// ory_kratos_session cookie upstream, resolves the Kratos identity
// to a panel user row, and returns { id (panel ULID), email, is_admin
// (DB-authoritative, not the Kratos trait) }.
//
// We DO NOT call Kratos /sessions/whoami directly. The Kratos
// identity UUID and the panel ULID are different values; any PATCH
// /users/:id or GET /users/:id/usage call needs the panel ULID.
// /me returns that — /sessions/whoami does not.
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";
import { logoutBrowser } from "../kratos";
import { setPanelClock } from "../utils/datetime";

export type MeUser = {
  id: string;
  email: string;
  isAdmin: boolean;
  fullName?: string;
  username?: string;
};

type MeResponse = {
  id: string;
  email: string;
  is_admin: boolean;
  /** GH #2164: the panel's clock (Server Settings timezone + date format). */
  timezone?: string;
  date_format?: string;
  full_name?: string;
  username?: string;
};

type AuthState = {
  user: MeUser | null;
  isAdmin: boolean;
  isLoading: boolean;
  refresh: () => Promise<void>;
  logout: () => Promise<string | null>;
};

const AuthContext = createContext<AuthState | null>(null);

async function fetchMe(): Promise<MeUser | null> {
  try {
    const { data } = await apiClient.get<MeResponse>("/me");
    if (!data?.id) return null;
    // GH #2164: set the panel clock here, before RequireAuth lets any page
    // render, so the first paint already shows server time in the admin's
    // date format.
    setPanelClock(data.timezone, data.date_format);
    return {
      id: data.id,
      email: data.email ?? "",
      isAdmin: data.is_admin === true,
      fullName: data.full_name,
      username: data.username,
    };
  } catch {
    // 401 (no session) and transient 5xx both collapse to null. Consumers
    // treat null as "not logged in" and RequireAuth redirects to /login.
    return null;
  }
}

export const AuthProvider = ({ children }: { children: ReactNode }) => {
  const qc = useQueryClient();

  const { data, isLoading, refetch } = useQuery<MeUser | null>({
    queryKey: ["whoami"],
    queryFn: fetchMe,
    retry: false,
    // whoami is the root of every protected render — keep it fresh
    // enough that role changes propagate without a hard reload, but
    // not so hungry that every tab focus hits the network.
    staleTime: 60_000,
  });

  const refresh = useCallback(async () => {
    await refetch();
  }, [refetch]);

  const logout = useCallback(async (): Promise<string | null> => {
    // Kratos browser-flow logout revokes the ory_kratos_session cookie via a
    // top-level navigation to logout_url (returned here for the caller to
    // navigate to). Best-effort — a stale cookie or an unreachable Kratos
    // shouldn't block client-side cleanup.
    let logoutURL: string | null = null;
    try {
      logoutURL = await logoutBrowser();
    } catch {
      /* noop */
    }
    qc.clear();
    // Wipe per-tab login redirect state so a future login from this
    // tab doesn't bounce to a stale return path (e.g. /profile from
    // a session that was closed mid-2FA-enrolment).
    sessionStorage.removeItem("post_login_return_to");
    sessionStorage.removeItem("kratos_settings_redirect_attempted");
    return logoutURL;
  }, [qc]);

  const value = useMemo<AuthState>(
    () => ({
      user: data ?? null,
      isAdmin: data?.isAdmin === true,
      isLoading,
      refresh,
      logout,
    }),
    [data, isLoading, refresh, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};

export const useAuth = (): AuthState => {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
};

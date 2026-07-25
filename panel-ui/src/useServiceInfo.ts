// useServiceInfo — TanStack Query hook for GET /info.
//
// Backend exposes demo-mode metadata under the `demo` key when
// cfg.Demo.Enabled is true; the SPA reads it once at boot to render
// (a) the "Enter as admin / user" buttons on the Login page and
// (b) the persistent DEMO banner above every shell.
//
// In production /info still returns version + endpoints + docs (no
// `demo` key), so `info.demo?.enabled` is the canonical "are we in
// demo mode" predicate.

import { useQuery } from "@tanstack/react-query";

export type ServiceInfoDemo = {
  enabled: boolean;
  admin_email?: string;
  admin_password?: string;
  user_email?: string;
  user_password?: string;
  banner?: string;
};

export type ServiceInfo = {
  service: string;
  version: string;
  status: string;
  endpoints?: Record<string, string>;
  docs?: string;
  demo?: ServiceInfoDemo;
};

async function fetchServiceInfo(): Promise<ServiceInfo> {
  const r = await fetch("/info", {
    headers: { Accept: "application/json" },
    credentials: "same-origin",
  });
  if (!r.ok) {
    throw new Error(`/info HTTP ${r.status}`);
  }
  return (await r.json()) as ServiceInfo;
}

export function useServiceInfo() {
  return useQuery({
    queryKey: ["service-info"],
    queryFn: fetchServiceInfo,
    // /info changes only on panel restart — long stale time is fine
    // and avoids one extra request per route change.
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
    retry: 1,
  });
}

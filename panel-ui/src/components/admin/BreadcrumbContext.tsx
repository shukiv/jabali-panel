// BreadcrumbContext — one breadcrumb per page (GH #455).
//
// The shell layout renders a single <RouteBreadcrumb>. By default it derives a
// trail from the current route + the nav table, so EVERY page gets a
// breadcrumb. A page with richer, entity-aware crumbs (e.g. Users > alice >
// Domains) calls useSetBreadcrumbs(...) to override the auto trail; the
// override clears on unmount so the next page falls back to the default.
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

import type { Crumb } from "./entityLinks";

type BreadcrumbCtx = {
  override: Crumb[] | null;
  setOverride: (c: Crumb[] | null) => void;
};

const Ctx = createContext<BreadcrumbCtx | null>(null);

export function BreadcrumbProvider({ children }: { children: ReactNode }) {
  const [override, setOverride] = useState<Crumb[] | null>(null);
  return <Ctx.Provider value={{ override, setOverride }}>{children}</Ctx.Provider>;
}

// useSetBreadcrumbs replaces the auto-derived trail with the given crumbs while
// the calling page is mounted. Pass null to keep the route-derived default
// (e.g. a list page that is entity-scoped only some of the time).
export function useSetBreadcrumbs(crumbs: Crumb[] | null) {
  const ctx = useContext(Ctx);
  const key = crumbs ? JSON.stringify(crumbs) : null;
  useEffect(() => {
    ctx?.setOverride(crumbs);
    return () => ctx?.setOverride(null);
    // Re-run only when the serialized crumbs change, not on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
}

export function useBreadcrumbOverride(): Crumb[] | null {
  return useContext(Ctx)?.override ?? null;
}

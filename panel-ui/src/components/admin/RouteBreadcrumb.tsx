// RouteBreadcrumb — the single breadcrumb the shell layout renders (GH #455).
//
// Uses the page's override crumbs when set (see BreadcrumbContext), otherwise
// derives a trail from the current pathname + the shell's nav table:
//   Home > <Section> > <segment> > … (last segment = current page, unlinked).
// Opaque id segments (ULIDs / long hex) are skipped in the default trail — an
// entity page that wants "Users > alice > Edit" supplies that via
// useSetBreadcrumbs instead.
import { useLocation } from "react-router";

import type { NavItem } from "../../nav";
import { AdminBreadcrumb } from "./AdminBreadcrumb";
import { useBreadcrumbOverride } from "./BreadcrumbContext";
import type { Crumb } from "./entityLinks";

const SEGMENT_LABELS: Record<string, string> = {
  create: "Create",
  new: "New",
  edit: "Edit",
  settings: "Settings",
  detail: "Details",
  details: "Details",
};

function titleCase(seg: string): string {
  const spaced = seg.replace(/-/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

// Opaque = a URL segment that is an identifier, not a page name: a ULID
// (26 Crockford base32 chars) or a long hex/uuid. These are skipped in the
// auto trail because they aren't human-readable.
function isOpaqueId(seg: string): boolean {
  return /^[0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26}$/.test(seg) || /^[0-9a-fA-F-]{16,}$/.test(seg);
}

export function deriveCrumbs(
  pathname: string,
  nav: NavItem[],
  homePath: string,
  homeLabel: string,
): Crumb[] {
  const crumbs: Crumb[] = [{ title: homeLabel, href: homePath }];

  // Longest matching nav path prefix = the section this page lives under.
  const section = [...nav]
    .sort((a, b) => b.path.length - a.path.length)
    .find((n) => pathname === n.path || pathname.startsWith(n.path + "/"));

  if (section && section.path !== homePath) {
    crumbs.push({ title: section.label, href: section.path });
    const rest = pathname.slice(section.path.length).split("/").filter(Boolean);
    for (const seg of rest) {
      if (isOpaqueId(seg)) continue;
      crumbs.push({ title: SEGMENT_LABELS[seg] ?? titleCase(seg) });
    }
  }

  // The last crumb is the current page — never a link.
  const last = crumbs.length - 1;
  crumbs[last] = { title: crumbs[last].title };
  return crumbs;
}

export function RouteBreadcrumb({
  nav,
  homePath,
  homeLabel,
}: {
  nav: NavItem[];
  homePath: string;
  homeLabel: string;
}) {
  const override = useBreadcrumbOverride();
  const { pathname } = useLocation();
  const items = override && override.length > 0 ? override : deriveCrumbs(pathname, nav, homePath, homeLabel);
  if (items.length === 0) return null;
  return <AdminBreadcrumb items={items} />;
}

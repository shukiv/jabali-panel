// userSidebarItems.tsx — pure transform from userNav leaves + userNavGroups
// into the AntD Menu `items` shape for the tenant sidebar (GH #1626).
//
// Kept out of UserLayout so the grouping/rendering rules are unit-testable
// without standing up the whole shell (router, i18n, capabilities). The
// leaves themselves still live in the flat `userNav`; this only arranges
// them into the four sections and decides how each section renders.
import type { ReactNode } from "react";
import type { MenuProps } from "antd";

import { navGroupForKey, userNavGroups, type NavItem } from "../nav";

export type SidebarMenuItem = NonNullable<MenuProps["items"]>[number];

export type BuildUserSidebarItemsOpts = {
  /** True for the 64px desktop icon rail; false for the expanded rail and
   *  the mobile Drawer. */
  isCollapsed: boolean;
  /** Visible leaves keyed by NavItem.key (already capability-filtered). */
  visibleByKey: Map<string, NavItem>;
  /** The currently selected leaf key, or undefined. */
  selectedKey?: string;
  /** Renders one leaf into its menu item (owns i18n label + count badge). */
  leafItem: (n: NavItem) => SidebarMenuItem;
  /** Translates an i18n key (e.g. a group label). */
  t: (key: string) => string;
  /** Color for a collapsible group header when it is NOT the active section
   *  — matches the muted always-open group-title color so Tools/Account read
   *  as headers, not nav items. */
  mutedHeaderColor: string;
};

// buildUserSidebarItems arranges the visible leaves into Dashboard +
// Hosting / Services / Tools / Account.
//
//   - Dashboard: standalone leaf, first.
//   - Hosting / Services (collapsible:false): always-open AntD `type:"group"`
//     — a muted, icon-less section title with its items always shown.
//   - Tools / Account (collapsible:true): a SubMenu, collapsed by default.
//
// The perceptual bug this fixes (GH #1626, johnnyq): an AntD `type:"group"`
// title has no closing boundary, and a default SubMenu title renders like a
// nav item (normal color, leading icon). So in the expanded rail the two
// collapsible SubMenus rendered after the last group read as items nested
// under the "Services" header. In the expanded rail we therefore render the
// collapsible group's title like a group header — muted, no leading icon,
// keeping only the chevron as the collapsible cue. When a child route is
// active we leave the color to AntD so the selected-section highlight still
// wins. The collapsed icon rail is unchanged: there the icon IS the
// affordance, so the SubMenu keeps it.
//
// An empty group (all its items gated off) drops out entirely.
export function buildUserSidebarItems(opts: BuildUserSidebarItemsOpts): SidebarMenuItem[] {
  const { isCollapsed, visibleByKey, selectedKey, leafItem, t, mutedHeaderColor } = opts;
  const items: SidebarMenuItem[] = [];

  const dashboard = visibleByKey.get("dashboard");
  if (dashboard) items.push(leafItem(dashboard));

  const activeGroupKey = selectedKey ? navGroupForKey(selectedKey)?.key : undefined;

  for (const g of userNavGroups) {
    const children = g.itemKeys
      .map((k) => visibleByKey.get(k))
      .filter((n): n is NavItem => !!n)
      .map(leafItem);
    if (children.length === 0) continue;

    if (isCollapsed) {
      // Icon rail: a divider fronts every section, then its icons (a
      // collapsible group keeps its SubMenu, which pops children out on hover).
      items.push({ type: "divider", key: `div-${g.key}` });
      if (g.collapsible) items.push({ key: g.key, icon: g.icon, label: t(g.label), children });
      else items.push(...children);
    } else if (g.collapsible) {
      const isActive = activeGroupKey === g.key;
      const label: ReactNode = (
        <span style={isActive ? undefined : { color: mutedHeaderColor }}>{t(g.label)}</span>
      );
      // No `icon` and no `type` — a SubMenu whose title reads as a header.
      items.push({ key: g.key, label, children });
    } else {
      items.push({ type: "group", key: g.key, label: t(g.label), children });
    }
  }

  return items;
}

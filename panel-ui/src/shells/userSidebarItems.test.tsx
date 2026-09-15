import { isValidElement } from "react";
import { Menu } from "antd";
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { userNav, type NavItem } from "../nav";
import {
  buildUserSidebarItems,
  type SidebarMenuItem,
} from "./userSidebarItems";

// GH #1626 (johnnyq follow-up): in the expanded tenant sidebar the collapsible
// groups Tools/Account rendered like nav items (leading icon, item color) after
// the Services group title, so they read as nested UNDER Services. They must
// render as their own section headers — icon-less, muted title, chevron kept.
// These assertions lock that: they fail on the pre-fix render (icon present,
// plain string label) and pass after.

const MUTED = "rgba(0,0,0,0.45)";

const visibleAll = (): Map<string, NavItem> =>
  new Map(userNav.map((n) => [n.key, n] as const));

// A leaf stub mirroring UserLayout.leafItem's shape (key + icon + label).
const leafItem = (n: NavItem): SidebarMenuItem => ({
  key: n.key,
  icon: n.icon,
  label: n.label,
});

const t = (k: string) => k;

const build = (isCollapsed: boolean, selectedKey?: string) =>
  buildUserSidebarItems({
    isCollapsed,
    visibleByKey: visibleAll(),
    selectedKey,
    leafItem,
    t,
    mutedHeaderColor: MUTED,
  });

// Narrow a menu item to a plain record so we can read optional fields
// (type/icon/children/label) without fighting AntD's union type.
const asRecord = (item: SidebarMenuItem | undefined): Record<string, unknown> => {
  expect(item).toBeTruthy();
  return item as unknown as Record<string, unknown>;
};

const find = (items: SidebarMenuItem[], key: string) =>
  asRecord(items.find((i) => (i as { key?: string })?.key === key));

describe("buildUserSidebarItems — expanded rail", () => {
  const items = build(false);

  it("renders Hosting and Services as always-open group headers", () => {
    for (const key of ["hosting", "services"]) {
      const g = find(items, key);
      expect(g.type).toBe("group");
      expect(g.icon).toBeUndefined();
      expect(Array.isArray(g.children)).toBe(true);
    }
  });

  it("renders Tools and Account as icon-less collapsible SubMenu headers", () => {
    for (const key of ["tools", "account"]) {
      const g = find(items, key);
      // SubMenu (has children) but NOT a group and NOT a leading icon — so it
      // reads as a header, not a nav item nested under Services.
      expect(g.type).toBeUndefined();
      expect(Array.isArray(g.children)).toBe(true);
      expect(g.icon).toBeUndefined();
    }
  });

  it("mutes the collapsible header title when no child route is active", () => {
    const tools = find(items, "tools");
    expect(isValidElement(tools.label)).toBe(true);
    const el = tools.label as React.ReactElement<{ style?: React.CSSProperties }>;
    expect(el.props.style?.color).toBe(MUTED);
  });

  it("leaves the header color to AntD when a child route IS active (selected highlight wins)", () => {
    const activeItems = build(false, "files"); // files ∈ Tools
    const tools = find(activeItems, "tools");
    const el = tools.label as React.ReactElement<{ style?: React.CSSProperties }>;
    expect(el.props.style).toBeUndefined();
  });
});

// GH #1626 (johnnyq second follow-up): the Tools/Account submenu HEADERS still
// sat ~12px right of the Hosting/Services group headers, so they read as
// indented. UserLayout tags the inline Menu `className="tenant-sidebar"` and
// global.css pulls `.tenant-sidebar.ant-menu-inline .ant-menu-submenu-title`
// left. happy-dom has no layout so the px alignment is verified in a real
// browser, not here; this locks the DOM contract that override relies on: the
// scope class lands on the inline-menu root, and Tools/Account really do render
// as `.ant-menu-submenu-title` targets. If either drifts (class dropped, groups
// re-typed), the CSS silently stops applying and this fails.
describe("expanded rail DOM — GH #1626 header-alignment CSS contract", () => {
  it("scope class lands on the inline menu root and targets the submenu titles", () => {
    const { container } = render(
      <Menu mode="inline" className="tenant-sidebar" items={build(false)} />,
    );
    const root = container.querySelector(".tenant-sidebar");
    expect(root).toBeTruthy();
    // Compound `.tenant-sidebar.ant-menu-inline` in global.css must match the
    // same element (excludes the collapsed .ant-menu-vertical rail).
    expect(root?.classList.contains("ant-menu-inline")).toBe(true);
    // Tools/Account render as submenu titles — the elements the rule shifts.
    const submenuTitles = container.querySelectorAll(
      ".tenant-sidebar.ant-menu-inline .ant-menu-submenu-title",
    );
    expect(submenuTitles.length).toBe(2);
  });
});

describe("buildUserSidebarItems — collapsed icon rail", () => {
  const items = build(true);

  it("keeps the icon on collapsible groups (the icon is the rail affordance)", () => {
    for (const key of ["tools", "account"]) {
      const g = find(items, key);
      expect(g.icon).toBeTruthy();
      expect(Array.isArray(g.children)).toBe(true);
    }
  });

  it("flattens always-open groups to their leaves behind a divider", () => {
    // No `hosting`/`services` header item in the icon rail; a divider fronts
    // each section and the leaves render directly.
    expect(items.some((i) => (i as { key?: string })?.key === "hosting")).toBe(false);
    expect(items.some((i) => (i as { type?: string })?.type === "divider")).toBe(true);
    // A hosting leaf (domains) renders directly.
    expect(items.some((i) => (i as { key?: string })?.key === "domains")).toBe(true);
  });
});

// UserLayout.tsx — chrome for the user shell.
//
// Same composition as AdminLayout (see that file for the "why"), but
// driven by `userNav` so an admin-only entry can never leak into the
// sidebar here.
import { useEffect, useState } from "react";
import { LeftOutlined, RightOutlined } from "@icons";
import { ConfigProvider, Drawer, Grid, Layout, Menu, theme, type MenuProps } from "antd";
import { Outlet, useLocation, useNavigate } from "react-router";

import { DRStandbyBanner } from "../components/DRStandbyBanner";
import { JabaliFooter } from "../components/JabaliFooter";
import { ImpersonationBanner } from "../components/ImpersonationBanner";
import { JabaliHeader } from "../components/JabaliHeader";
import { JabaliTitle } from "../components/JabaliTitle";
import { useTranslation } from "react-i18next";

import { navGroupForKey, selectedNavKey, userNav, userNavGroups, type NavItem } from "../nav";
import { BreadcrumbProvider } from "../components/admin/BreadcrumbContext";
import { RouteBreadcrumb } from "../components/admin/RouteBreadcrumb";
import { useThemeMode } from "../theme/ThemeModeContext";
import { useServerCapabilities } from "../hooks/useServerCapabilities";
import { useNavCounts, navCountForKey } from "../hooks/useNavCounts";
import { navLabelWithBadge } from "./navBadge";
import { QuickStartModal } from "./user/QuickStartModal";

const { Sider, Content } = Layout;

export function UserLayout() {
  const [collapsed, setCollapsed] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  // Which collapsible groups (Tools / Account) are expanded. Controlled so
  // navigating into a group auto-opens it (see the effect below) while the
  // user stays free to close it. Always-open groups (Hosting / Services)
  // render as AntD `type:"group"` and never appear here.
  const [openKeys, setOpenKeys] = useState<string[]>([]);
  const location = useLocation();
  const navigate = useNavigate();
  const { mode } = useThemeMode();
  const { token } = theme.useToken();
  const screens = Grid.useBreakpoint();
  // screens.lg is undefined on the first render before AntD measures the
  // viewport. Fall back to window.innerWidth so mobile users see the
  // hamburger on initial paint rather than the desktop Sider.
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  // Python Apps is opt-in (server setting python_apps_enabled, default off);
  // hide its sidebar entry until an admin enables it (GH #229). The same
  // cached capability gates the route itself (CapabilityRoute, gap-audit #1).
  const { data: caps } = useServerCapabilities();
  // GH #1478: side-nav badge counts (tenant scope).
  const { data: navCounts } = useNavCounts("me");
  // nav.label holds an i18n key (see src/locales/en/common.json).
  const { t } = useTranslation();
  const visibleNav = userNav.filter((n) => {
    if (n.key === "python-apps") return !!caps?.python_apps_enabled;
    if (n.key === "docker-apps") return !!caps?.docker_apps_user_enabled;
    // GH #1053: package-gated (max_ftp_accounts > 0) — hidden by default.
    if (n.key === "ftp-accounts") return !!caps?.ftp_accounts_enabled;
    // M353 Phase 1 (GH #353): module flags default on (undefined = shown).
    if (n.key === "mail") return caps?.mail_enabled !== false;
    if (n.key === "dns") return caps?.dns_enabled !== false;
    if (n.key === "api-tokens") return caps?.api_enabled !== false;
    return true;
  });

  const selected = selectedNavKey(visibleNav, location.pathname);

  // siderBg follows colorBgLayout in both modes so the operator page/sidebar
  // chrome color (GH #435) applies; muiTheme sets the light default + override.
  const siderBg = token.colorBgLayout;

  // GH #1626: the sidebar is grouped (Hosting / Services / Tools / Account)
  // rather than a flat list. The leaves still come from the flat `userNav`
  // (via visibleNav) so capability gating and count badges are unchanged;
  // `userNavGroups` only arranges them. An empty group (all its items gated
  // off) drops out entirely.
  const visibleByKey = new Map(visibleNav.map((n) => [n.key, n] as const));
  const leafItem = (n: NavItem) => ({
    key: n.key,
    icon: n.icon,
    label: navLabelWithBadge(t(n.label), navCountForKey(navCounts, n.key)),
    onClick: () => {
      navigate(n.path);
      setDrawerOpen(false);
    },
  });
  // Build the menu items. When the desktop sider is collapsed to the 64px
  // icon rail, the always-open groups flatten to plain icon rows separated
  // by dividers (a `type:"group"` header carries no icon, so it has no
  // icon-rail form); the collapsible groups stay as SubMenus, which AntD
  // turns into hover-popouts when collapsed. The mobile Drawer always
  // builds in the expanded (collapsed=false) shape.
  const buildItems = (isCollapsed: boolean): NonNullable<MenuProps["items"]> => {
    const items: NonNullable<MenuProps["items"]> = [];
    const dashboard = visibleByKey.get("dashboard");
    if (dashboard) items.push(leafItem(dashboard));
    for (const g of userNavGroups) {
      const children = g.itemKeys
        .map((k) => visibleByKey.get(k))
        .filter((n): n is NavItem => !!n)
        .map(leafItem);
      if (children.length === 0) continue;
      if (isCollapsed) {
        // Icon rail: a divider fronts every section, then its icons (a
        // collapsible group keeps its SubMenu, which pops children out).
        items.push({ type: "divider", key: `div-${g.key}` });
        if (g.collapsible) items.push({ key: g.key, icon: g.icon, label: t(g.label), children });
        else items.push(...children);
      } else if (g.collapsible) {
        items.push({ key: g.key, icon: g.icon, label: t(g.label), children });
      } else {
        items.push({ type: "group", key: g.key, label: t(g.label), children });
      }
    }
    return items;
  };

  // User panel takes the AntD-default blue accent on the selected menu
  // row; admin keeps red (set globally in muiTheme.ts). The nested
  // ConfigProvider overlays the Menu tokens for this shell only —
  // header, footer, tabs, and buttons still read the red accent from
  // the top-level provider because they inherit outside this wrap.
  const renderMenu = (isCollapsed: boolean) => (
    <ConfigProvider
      theme={{
        components: {
          Menu:
            mode === "dark"
              ? {
                  darkItemSelectedBg: "#1f1f1f",
                  darkItemSelectedColor: "#4096ff",
                  darkItemHoverBg: "#1f1f1f",
                  darkItemHoverColor: "rgba(255, 255, 255, 0.85)",
                }
              : {
                  itemSelectedBg: "#f3f4f6",
                  itemSelectedColor: "#1677ff",
                  itemHoverBg: "#f3f4f6",
                  itemHoverColor: "rgba(0, 0, 0, 0.88)",
                },
        },
      }}
    >
      <Menu
        mode="inline"
        theme={mode}
        selectedKeys={selected ? [selected] : []}
        // Collapsed inline menus manage their own popup open-state; only
        // control openKeys in the expanded rail. rc-menu also fires
        // onOpenChange([]) as the sider collapses — ignoring events while
        // collapsed keeps that from wiping the expanded open-state, so the
        // active group is still open on re-expand.
        openKeys={isCollapsed ? undefined : openKeys}
        onOpenChange={(keys) => {
          if (!isCollapsed) setOpenKeys(keys);
        }}
        style={{ border: "none", background: siderBg }}
        items={buildItems(isCollapsed)}
      />
    </ConfigProvider>
  );

  useEffect(() => {
    setDrawerOpen(false);
  }, [location.pathname]);

  // Auto-open the collapsible group that owns the active route so a deep
  // link into Tools/Account lands with its group expanded. Scoped to the
  // active group's key (not openKeys) so re-running never fights a user who
  // just closed a different group.
  const activeGroup = selected ? navGroupForKey(selected) : undefined;
  const activeCollapsibleGroupKey =
    activeGroup?.collapsible ? activeGroup.key : undefined;
  useEffect(() => {
    if (activeCollapsibleGroupKey) {
      setOpenKeys((keys) =>
        keys.includes(activeCollapsibleGroupKey)
          ? keys
          : [...keys, activeCollapsibleGroupKey],
      );
    }
  }, [activeCollapsibleGroupKey]);

  return (
    <Layout style={{ minHeight: "100dvh" }}>
      <ImpersonationBanner />
      <JabaliHeader
        showMenuButton={!isDesktop}
        onMenuClick={() => setDrawerOpen(true)}
        searchNav={visibleNav}
      />
      <Layout>
        {isDesktop ? (
          <Sider
            theme={mode}
            width={256}
            breakpoint="lg"
            collapsedWidth="64"
            collapsible
            collapsed={collapsed}
            onCollapse={setCollapsed}
            trigger={
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  width: "100%",
                  height: "100%",
                  color: token.colorTextSecondary,
                  background: siderBg,
                }}
              >
                {collapsed ? <RightOutlined /> : <LeftOutlined />}
              </div>
            }
            style={{
              background: siderBg,
              paddingTop: 16,
              paddingInline: 8,
              height: "100vh",
              position: "sticky",
              top: 0,
              overflow: "hidden",
            }}
          >
            <div
              style={{
                height: "100%",
                overflowY: "auto",
                overflowX: "hidden",
                paddingBottom: 48,
              }}
            >
              {renderMenu(collapsed)}
            </div>
          </Sider>
        ) : drawerOpen ? (
          <Drawer
            open
            onClose={() => setDrawerOpen(false)}
            placement="left"
            width={256}
            closable
            title={<JabaliTitle />}
            // GH #1066: mount the nav Drawer only while it's open. On dismiss it
            // unmounts on the same tick, so there's no leave animation holding
            // AntD v6's full-viewport `position: fixed; inset: 0` portal in the
            // DOM — that lingering portal was the residual bottom bar (#1250),
            // and the ~1s the exit animation kept it mounted was the remaining
            // delay before the mobile layout reclaimed the space. The slide-in
            // on open is preserved (appear motion still plays on mount).
            // destroyOnHidden stays as a belt-and-braces net.
            destroyOnHidden
            styles={{
              body: { padding: 8, background: siderBg },
              header: { background: siderBg },
            }}
          >
            {renderMenu(false)}
          </Drawer>
        ) : null}
        <Layout>
          <Content
            style={{
              padding: screens.md ? "32px 24px 24px" : "20px 12px 12px",
              // minWidth:0 lets this flex child shrink; overflowX hidden is
              // the backstop so a single wide element can't sideways-scroll
              // the page on mobile (tables keep their own inner scroll).
              minWidth: 0,
              overflowX: "hidden",
            }}
          >
            <DRStandbyBanner />
            <BreadcrumbProvider>
              <RouteBreadcrumb nav={userNav} homePath="/jabali-panel/dashboard" homeLabel="Dashboard" />
              <Outlet />
            </BreadcrumbProvider>
            <QuickStartModal />
          </Content>
          <JabaliFooter />
        </Layout>
      </Layout>
    </Layout>
  );
}

// AdminLayout.tsx — chrome for the admin shell.
//
// Full-width Header on top (brand + search + user menu), then either
// a persistent <Sider> (≥lg / 992px) or an off-canvas <Drawer> (<lg)
// that the header's hamburger button opens. See ADR-0046.
import { useEffect, useState } from "react";
import { LeftOutlined, RightOutlined } from "@icons";
import { Drawer, Grid, Layout, Menu, Tooltip, theme } from "antd";
import { Outlet, useLocation, useNavigate } from "react-router";

import { useServerCapabilities } from "../hooks/useServerCapabilities";
import { DRStandbyBanner } from "../components/DRStandbyBanner";
import { JabaliFooter } from "../components/JabaliFooter";
import { JabaliHeader } from "../components/JabaliHeader";
import { JabaliTitle } from "../components/JabaliTitle";
import { useTranslation } from "react-i18next";

import { adminNav, selectedNavKey } from "../nav";
import { BreadcrumbProvider } from "../components/admin/BreadcrumbContext";
import { RouteBreadcrumb } from "../components/admin/RouteBreadcrumb";
import { useThemeMode } from "../theme/ThemeModeContext";
import { QuickStartModal } from "./admin/QuickStartModal";

const { Sider, Content } = Layout;

export function AdminLayout() {
  const [collapsed, setCollapsed] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const location = useLocation();
  const navigate = useNavigate();
  const { mode } = useThemeMode();
  const { token } = theme.useToken();
  const screens = Grid.useBreakpoint();
  // screens.lg is undefined on the first render before AntD measures the
  // viewport. Fall back to window.innerWidth so mobile users see the
  // hamburger on initial paint rather than the desktop Sider.
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  // M48: hide the Docker Apps nav entry until the operator opts in
  // via Server Settings -> Apps. /me/server-capabilities is cached
  // per-session; UI ergonomics outweigh staleness here.
  const { data: caps } = useServerCapabilities();
  // M353 Phase 1 (GH #353): hide a module's nav entry when it's disabled.
  // Module flags default on (undefined/loading = shown) so an upgraded install
  // and the brief pre-caps render never flash-hide a real feature.
  const visibleNav = adminNav.filter((n) => {
    if (n.key === "docker-apps") return !!caps?.docker_marketplace_enabled;
    // GH #515: default-off module — hide until explicitly enabled (like docker-apps).
    if (n.key === "terminal") return !!caps?.root_terminal_enabled;
    // GH #1184: default-off admin File Manager — hide until enabled.
    if (n.key === "admin-files") return !!caps?.admin_file_manager_enabled;
    if (n.key === "mail") return caps?.mail_enabled !== false;
    if (n.key === "dns") return caps?.dns_enabled !== false;
    if (n.key === "security") return caps?.security_enabled !== false;
    if (n.key === "api-tokens") return caps?.api_enabled !== false;
    return true;
  });

  const selected = selectedNavKey(visibleNav, location.pathname);

  // Light mode: explicit Tailwind gray-50 / gray-100 per operator request
  // so the sidebar sits a shade paler than the main card surface and the
  // active menu row reads slightly darker than the sidebar body. Dark
  // mode keeps the layout-bg token (it already pairs well with the
  // algorithm-derived itemSelectedBg).
  // siderBg follows colorBgLayout in both modes so the operator page/sidebar
  // chrome color (GH #435) applies; muiTheme sets the light default + override.
  const siderBg = token.colorBgLayout;
  // nav.label / nav.description hold i18n keys (see src/locales/en/common.json).
  const { t } = useTranslation();

  // Single source of truth for the menu items — used by both <Sider>
  // and <Drawer> so the two shell variants stay in lock-step.
  const menu = (
    <Menu
      mode="inline"
      theme={mode}
      selectedKeys={selected ? [selected] : []}
      style={{ border: "none", background: siderBg }}
      items={visibleNav.map((n) => ({
        key: n.key,
        icon: n.icon,
        // Right-placed hover tooltip explaining the tab (mouseEnterDelay keeps it
        // from flashing while scanning the list). Falls back to the plain label
        // when an item has no description. DESKTOP ONLY (GH #1066): on touch the
        // tooltip swallows the first tap — the user has to tap twice to navigate.
        // The mobile Drawer shows full labels, so the tooltip adds nothing there.
        label: isDesktop && n.description ? (
          <Tooltip title={t(n.description)} placement="right" mouseEnterDelay={0.4}>
            <span>{t(n.label)}</span>
          </Tooltip>
        ) : (
          t(n.label)
        ),
        onClick: () => {
          navigate(n.path);
          setDrawerOpen(false);
        },
      }))}
    />
  );

  // Close the drawer on every route change — covers not just menu
  // clicks but also back-button / programmatic navigation.
  useEffect(() => {
    setDrawerOpen(false);
  }, [location.pathname]);

  return (
    // GH #1066: desktop keeps the fixed app-shell (header + sidebar pinned, only
    // Content scrolls). On mobile that pins the version footer to the bottom of a
    // viewport-height scroll box, eating screen space on short pages — so mobile
    // uses natural document scroll (minHeight) and the footer flows after content.
    <Layout style={isDesktop ? { height: "100vh", overflow: "hidden" } : { minHeight: "100vh" }}>
      <JabaliHeader
        showMenuButton={!isDesktop}
        onMenuClick={() => setDrawerOpen(true)}
        searchNav={visibleNav}
      />
      {/* Row sized to the viewport minus the 64px header, so the sidebar +
          header stay fixed and only the Content region scrolls (desktop only). */}
      <Layout style={isDesktop ? { height: "calc(100vh - 64px)" } : undefined}>
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
              // Fills the fixed-height row; its inner div scrolls a long menu.
              // The row doesn't scroll, so the sidebar stays put while Content
              // scrolls independently.
              height: "100%",
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
              {menu}
            </div>
          </Sider>
        ) : (
          <Drawer
            open={drawerOpen}
            onClose={() => setDrawerOpen(false)}
            placement="left"
            width={256}
            closable
            title={<JabaliTitle />}
            styles={{
              body: { padding: 8, background: siderBg },
              header: { background: siderBg },
            }}
          >
            {menu}
          </Drawer>
        )}
        <Layout style={isDesktop ? { height: "100%", overflowY: "auto" } : undefined}>
          <Content
            style={{
              // Extra top gap so the page heading breathes away from
              // the header's bottom border. Horizontal + bottom stay
              // at the baseline gutter.
              padding: screens.md ? "32px 24px 24px" : "20px 12px 12px",
              // minWidth:0 lets this flex child shrink instead of forcing
              // the page wider than the viewport; overflowX hidden is the
              // backstop so a single wide element can't sideways-scroll the
              // whole page on mobile (tables keep their own inner scroll).
              minWidth: 0,
              overflowX: "hidden",
            }}
          >
            <DRStandbyBanner />
            <BreadcrumbProvider>
              <RouteBreadcrumb nav={adminNav} homePath="/jabali-admin/dashboard" homeLabel="Dashboard" />
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

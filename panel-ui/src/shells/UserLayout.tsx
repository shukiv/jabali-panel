// UserLayout.tsx — chrome for the user shell.
//
// Same composition as AdminLayout (see that file for the "why"), but
// driven by `userNav` so an admin-only entry can never leak into the
// sidebar here.
import { useEffect, useState } from "react";
import { LeftOutlined, RightOutlined } from "@icons";
import { ConfigProvider, Drawer, Grid, Layout, Menu, theme } from "antd";
import { Outlet, useLocation, useNavigate } from "react-router";

import { DRStandbyBanner } from "../components/DRStandbyBanner";
import { JabaliFooter } from "../components/JabaliFooter";
import { ImpersonationBanner } from "../components/ImpersonationBanner";
import { JabaliHeader } from "../components/JabaliHeader";
import { JabaliTitle } from "../components/JabaliTitle";
import { useTranslation } from "react-i18next";

import { selectedNavKey, userNav } from "../nav";
import { BreadcrumbProvider } from "../components/admin/BreadcrumbContext";
import { RouteBreadcrumb } from "../components/admin/RouteBreadcrumb";
import { useThemeMode } from "../theme/ThemeModeContext";
import { useServerCapabilities } from "../hooks/useServerCapabilities";
import { QuickStartModal } from "./user/QuickStartModal";

const { Sider, Content } = Layout;

export function UserLayout() {
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

  // Python Apps is opt-in (server setting python_apps_enabled, default off);
  // hide its sidebar entry until an admin enables it (GH #229). The same
  // cached capability gates the route itself (CapabilityRoute, gap-audit #1).
  const { data: caps } = useServerCapabilities();
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

  // User panel takes the AntD-default blue accent on the selected menu
  // row; admin keeps red (set globally in muiTheme.ts). The nested
  // ConfigProvider overlays the Menu tokens for this shell only —
  // header, footer, tabs, and buttons still read the red accent from
  // the top-level provider because they inherit outside this wrap.
  const menu = (
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
        style={{ border: "none", background: siderBg }}
        items={visibleNav.map((n) => ({
          key: n.key,
          icon: n.icon,
          label: t(n.label),
          onClick: () => {
            navigate(n.path);
            setDrawerOpen(false);
          },
        }))}
      />
    </ConfigProvider>
  );

  useEffect(() => {
    setDrawerOpen(false);
  }, [location.pathname]);

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
              {menu}
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
            {menu}
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

// App root — AntD + TanStack Query + React Router with two fully
// separated shells.
//
// URL layout:
//   /login                       → LoginPage (Kratos-driven, public)
//   /consent                     → OAuth consent screen (auth required)
//   /jabali-admin/*              → AdminShell  (admins only, gated)
//   /jabali-panel/*              → UserShell   (authenticated, gated)
//   /                            → role-based redirect
//
// Why two shells instead of one role-filtered tree:
//   - No runtime risk of an admin menu item rendering for a user (the
//     two shells use distinct, hardcoded sidebars).
//   - URLs themselves are segregated, so browser history, bookmarks,
//     and any future access logs make the two surfaces unambiguous.
//   - Adding an admin page can't accidentally add a user page.
//
// Refine is gone as of M21: the tree composes QueryClientProvider +
// AuthProvider + BrowserRouter + ConfigProvider directly. Every
// protected page re-uses the same whoami cache.
import { QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Route, Routes } from "react-router";
import { App as AntdApp, ConfigProvider, Empty, Spin } from "antd";
import type { ReactNode } from "react";
import { useEffect } from "react";

import { AuthProvider, useAuth } from "./auth/AuthContext";
import { RequireAdmin } from "./auth/RequireAdmin";
import { RequireUser } from "./auth/RequireUser";
import useMuiTheme from "./muiTheme";
import { queryClient } from "./query";
import { AdminLayout } from "./shells/AdminLayout";
import { UserLayout } from "./shells/UserLayout";
import { LandingRedirect } from "./shells/LandingRedirect";
import { ThemeModeProvider, useThemeMode } from "./theme/ThemeModeContext";
import { Dashboard } from "./shells/admin/Dashboard";
import { UserList } from "./shells/admin/users/UserList";
import { AdminUserOverview } from "./shells/admin/users/AdminUserOverview";
import { AdminIPList } from "./shells/admin/ips/AdminIPList";
import { AdminMailPage } from "./shells/admin/mail/AdminMailPage";
import { AdminAuditList } from "./shells/admin/audit/AdminAuditList";
import { NotificationsTabsPage } from "./shells/admin/notifications/NotificationsTabsPage";
import { useApplyBrandingToTitle, useBranding } from "./hooks/useBranding";
import { PANEL_COLORS } from "./lib/panelColors";
import { AdminSecurityPage } from "./shells/admin/security/AdminSecurityPage";
import { ServerStatusPage } from "./shells/admin/server-status/ServerStatusPage";
import { CacheOverviewPage } from "./shells/admin/cache/CacheOverviewPage";
import { MailDeliverabilityPage } from "./shells/admin/mail/MailDeliverabilityPage";
import { MailThrottlesPage } from "./shells/admin/mail/MailThrottlesPage";
import { SystemUpdatesPage } from "./shells/admin/updates/SystemUpdatesPage";
import { SupportPage } from "./shells/admin/support/SupportPage";
import { AdminAutomationTokensPage } from "./shells/admin/automation/AdminAutomationTokensPage";
import { AdminMigrationsPage } from "./shells/admin/migrations/AdminMigrationsPage";
import { AdminMigrationDetailPage } from "./shells/admin/migrations/AdminMigrationDetailPage";
import { AdminBackupsPage } from "./shells/admin/backups/AdminBackupsPage";
import { PackageCreate } from "./shells/admin/packages/PackageCreate";
import { PackageEdit } from "./shells/admin/packages/PackageEdit";
import { PackageList } from "./shells/admin/packages/PackageList";
import { DomainCreate } from "./shells/admin/domains/DomainCreate";
import { DomainEdit } from "./shells/admin/domains/DomainEdit";
import { DomainList } from "./shells/admin/domains/DomainList";
import { ServerSettingsPage } from "./shells/admin/settings/ServerSettingsPage";
import { AdminTerminal } from "./shells/admin/terminal/AdminTerminal";
import { MyProfile } from "./shells/user/MyProfile";
import { UserDashboard } from "./shells/user/UserDashboard";
import { FileManagerPage } from "./shells/user/files/FileManagerPage";
import { UserDomainList } from "./shells/user/domains/UserDomainList";
import { UserDatabasesPage } from "./shells/user/databases/UserDatabasesPage";
import { DNSRecordsPage } from "./shells/dns/DNSRecordsPage";
import { DNSZonesOverviewPage } from "./shells/admin/dns/DNSZonesOverviewPage";
import { UserDNSZonesOverviewPage } from "./shells/user/dns/UserDNSZonesOverviewPage";
import { SSLManagerPage } from "./shells/admin/ssl/SSLManagerPage";
import { UserSSLManagerPage } from "./shells/user/ssl/UserSSLManagerPage";
import { UserPHPSettingsPage } from "./shells/user/php-settings/UserPHPSettingsPage";
import { UserApplicationList } from "./shells/user/applications/UserApplicationList";
import { DiskUsagePage } from "./shells/user/disk-usage/DiskUsagePage";
import { CapabilityRoute } from "./components/CapabilityRoute";
import { PythonAppsPage } from "./shells/user/python-apps/PythonAppsPage";
import { UserDockerAppsPage } from "./shells/user/docker-apps/UserDockerAppsPage";
import { UserBackupsPage } from "./shells/user/backups/UserBackupsPage";
import { UserLogsPage } from "./shells/user/logs/UserLogsPage";
import { UserSSHKeysPage } from "./shells/user/ssh-keys/UserSSHKeysPage";
import { UserAPITokensPage } from "./shells/user/api-tokens/UserAPITokensPage";
import { APIDocsPage } from "./shells/shared/APIDocsPage";
import { UserCronList } from "./shells/user/cron/UserCronList";
import { AdminCronList } from "./shells/admin/cron/AdminCronList";
import { MailTabsPage } from "./shells/user/mail/MailTabsPage";
import { AdminApplicationList } from "./shells/admin/applications/AdminApplicationList";
import { AdminDockerAppsPage } from "./shells/admin/docker-apps/AdminDockerAppsPage";
import { LogsPage } from "./shells/admin/logs/LogsPage";
import { PHPVersionsPage } from "./shells/admin/php/PHPVersionsPage";
import { PHPPoolEdit } from "./shells/admin/php-pools/PHPPoolEdit";
import { LoginPage } from "./pages/Login";

// If a logged-in user hits /login, bounce them to their shell home
// instead of letting them see the form. Public routes use this — the
// Kratos-driven LoginPage itself doesn't know about AuthContext, so
// the gate lives here.
// KratosSettingsRedirect — preserves ?flow=<id> when bouncing from
// the Kratos settings ui_url (configured as /settings in kratos.yml)
// to whichever shell-scoped profile route the user belongs to.
// Both /jabali-admin/profile and /jabali-panel/profile mount the same
// MyProfile component which then fetches the flow + renders inline.
function KratosSettingsRedirect() {
  const { isAdmin } = useAuth();
  const search = window.location.search;
  const target = isAdmin ? "/jabali-admin/profile" : "/jabali-panel/profile";
  return <Navigate to={`${target}${search}`} replace />;
}

function PublicOnly({ children }: { children: ReactNode }) {
  const { user, isLoading } = useAuth();
  if (isLoading) {
    return (
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          minHeight: "100vh",
        }}
      >
        <Spin size="large" />
      </div>
    );
  }
  if (user) {
    return (
      <Navigate to={user.isAdmin ? "/jabali-admin" : "/jabali-panel"} replace />
    );
  }
  return <>{children}</>;
}

const BrandingTitleApplier = () => {
  useApplyBrandingToTitle();
  return null;
};

const PANEL_FONT_PX: Record<string, number> = { small: 13, medium: 15, large: 17 };

const ThemedApp = () => {
  const { mode } = useThemeMode();
  const { fontSize, colors, chrome } = useBranding();
  // Branding "Look and feel": font size + operator colors feed the antd
  // ConfigProvider seed tokens so the whole panel scales/recolors for everyone.
  const seedColors: Record<string, string> = {};
  for (const c of PANEL_COLORS) {
    if (c.token && colors[c.field]) seedColors[c.token] = colors[c.field];
  }
  // Per-theme chrome colors for the active mode (GH #435). Empty = default.
  const chromeKey = (base: string) => chrome[`panel_${mode}_${base}_color`] || "";
  const headerBg = chromeKey("topbar");
  useEffect(() => {
    const root = document.documentElement;
    if (headerBg) root.style.setProperty("--jabali-header-bg", headerBg);
    else root.style.removeProperty("--jabali-header-bg");
  }, [headerBg]);
  const muiConfig = useMuiTheme(mode, {
    fontSizePx: PANEL_FONT_PX[fontSize] ?? 15,
    accentColor: colors.panel_accent_color || "",
    seedColors,
    chrome: {
      bg: chromeKey("bg"),
      container: chromeKey("container"),
      text: chromeKey("text"),
    },
  });

  return (
    <BrowserRouter>
      <ConfigProvider
        {...muiConfig}
        renderEmpty={() => <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />}
      >
        <AntdApp>
        <BrandingTitleApplier />
        <Routes>
          {/* ---------------- admin shell ---------------- */}
          <Route
            path="/jabali-admin"
            element={
              <RequireAdmin>
                <AdminLayout />
              </RequireAdmin>
            }
          >
            <Route index element={<Navigate to="dashboard" replace />} />
            <Route path="dashboard" element={<Dashboard />} />
            <Route path="cache" element={<CacheOverviewPage />} />
            {/* JAB-126: Sessions moved into the Users page as a tab.
                Preserve old bookmarks/deep-links. */}
            <Route
              path="sessions"
              element={
                <Navigate to="/jabali-admin/users?tab=sessions" replace />
              }
            />
            <Route path="users">
              <Route index element={<UserList />} />
              {/* legacy /create + /edit/:id redirect to list — Drawer
                  is the new create/edit surface (M28 follow-up). */}
              <Route path="create" element={<Navigate to="/jabali-admin/users" replace />} />
              <Route path="edit/:id" element={<Navigate to="/jabali-admin/users" replace />} />
              {/* #483: per-user hub — declared after create so it can't shadow it. */}
              <Route path=":id" element={<AdminUserOverview />} />
            </Route>
            <Route path="packages">
              <Route index element={<PackageList />} />
              <Route path="create" element={<PackageCreate />} />
              <Route path="edit/:id" element={<PackageEdit />} />
            </Route>
            <Route path="domains">
              <Route index element={<DomainList />} />
              <Route path="create" element={<DomainCreate />} />
              <Route path="edit/:id" element={<DomainEdit />} />
              <Route path=":id/dns" element={<DNSRecordsPage />} />
            </Route>
            <Route
              path="dns"
              element={
                <CapabilityRoute cap="dns_enabled" fallback="/jabali-admin/dashboard">
                  <DNSZonesOverviewPage />
                </CapabilityRoute>
              }
            />
            <Route path="ssl" element={<SSLManagerPage />} />
            <Route path="settings" element={<ServerSettingsPage />} />
            <Route path="php-pools">
              <Route index element={<PHPVersionsPage />} />
              <Route path="edit/:id" element={<PHPPoolEdit />} />
            </Route>
            <Route
              path="mail"
              element={
                <CapabilityRoute cap="mail_enabled" fallback="/jabali-admin/dashboard">
                  <AdminMailPage />
                </CapabilityRoute>
              }
            />
            <Route path="applications" element={<AdminApplicationList />} />
            <Route
              path="docker-apps"
              element={
                <CapabilityRoute cap="docker_marketplace_enabled" fallback="/jabali-admin/dashboard">
                  <AdminDockerAppsPage />
                </CapabilityRoute>
              }
            />
            <Route path="logs" element={<LogsPage />} />
            <Route path="audit" element={<AdminAuditList />} />
            <Route path="cron" element={<AdminCronList />} />
            <Route path="ips">
              <Route index element={<AdminIPList />} />
              <Route path="create" element={<Navigate to="/jabali-admin/ips" replace />} />
              <Route path="edit/:id" element={<Navigate to="/jabali-admin/ips" replace />} />
            </Route>
            <Route
              path="security"
              element={
                <CapabilityRoute cap="security_enabled" fallback="/jabali-admin/dashboard">
                  <AdminSecurityPage />
                </CapabilityRoute>
              }
            />
            <Route path="server-status" element={<ServerStatusPage />} />
            <Route path="mail/deliverability" element={<MailDeliverabilityPage />} />
            <Route path="mail/throttles" element={<MailThrottlesPage />} />
            <Route path="terminal" element={<AdminTerminal />} />
            <Route path="dnssec" element={<Navigate to="/jabali-admin/dns" replace />} />
            <Route path="updates" element={<SystemUpdatesPage />} />
            <Route path="support" element={<SupportPage />} />
            <Route path="automation" element={<AdminAutomationTokensPage />} />
            <Route path="migrations" element={<AdminMigrationsPage />} />
            <Route path="migrations/:id" element={<AdminMigrationDetailPage />} />
            <Route path="backups" element={<AdminBackupsPage />} />
            <Route path="notifications">
              <Route index element={<Navigate to="channels" replace />} />
              <Route path=":tab" element={<NotificationsTabsPage />} />
            </Route>
            {/* Admin profile — same MyProfile component as user shell.
                The header dropdown's "Profile" item routes here for
                admins; without this route RequireUser would bounce
                them back to /jabali-admin and the click did nothing.
                MyProfile is shell-agnostic: it hosts the Kratos
                settings flow inline (password / TOTP / recovery
                codes) which works for any authenticated session. */}
            <Route path="profile" element={<MyProfile />} />
            <Route path="api-docs" element={<APIDocsPage />} />
            <Route
              path="api-tokens"
              element={
                <CapabilityRoute cap="api_enabled" fallback="/jabali-admin/dashboard">
                  <UserAPITokensPage />
                </CapabilityRoute>
              }
            />
          </Route>

          {/* ---------------- user shell ----------------- */}
          <Route
            path="/jabali-panel"
            element={
              <RequireUser>
                <UserLayout />
              </RequireUser>
            }
          >
            <Route index element={<Navigate to="dashboard" replace />} />
            <Route path="dashboard" element={<UserDashboard />} />
            {/* Profile stays reachable via the header dropdown — no
                longer a sidebar entry as of the dashboard addition. */}
            <Route path="profile" element={<MyProfile />} />
            <Route path="domains">
              <Route index element={<UserDomainList />} />
              <Route path="create" element={<Navigate to="../domains" replace />} />
              <Route path=":id/dns" element={<DNSRecordsPage />} />
            </Route>
            <Route path="databases">
              <Route index element={<UserDatabasesPage />} />
              <Route path="create" element={<Navigate to="../databases" replace />} />
            </Route>
            <Route path="database-users">
              <Route path="create" element={<Navigate to="../databases" replace />} />
            </Route>
            <Route
              path="dns"
              element={
                <CapabilityRoute cap="dns_enabled" fallback="/jabali-panel/dashboard">
                  <UserDNSZonesOverviewPage />
                </CapabilityRoute>
              }
            />
            <Route path="ssl" element={<UserSSLManagerPage />} />
            <Route path="dnssec" element={<Navigate to="/jabali-panel/dns" replace />} />
            <Route path="php-settings" element={<UserPHPSettingsPage />} />
            <Route path="files" element={<FileManagerPage />} />
            <Route path="disk-usage" element={<DiskUsagePage />} />
            <Route path="logs" element={<UserLogsPage />} />
            <Route path="activity" element={<Navigate to="/jabali-panel/logs?tab=activity" replace />} />
            <Route path="applications" element={<UserApplicationList />} />
            <Route
              path="python-apps"
              element={
                <CapabilityRoute cap="python_apps_enabled" fallback="/jabali-panel/dashboard">
                  <PythonAppsPage />
                </CapabilityRoute>
              }
            />
            <Route
              path="docker-apps"
              element={
                <CapabilityRoute cap="docker_apps_user_enabled" fallback="/jabali-panel/dashboard">
                  <UserDockerAppsPage />
                </CapabilityRoute>
              }
            />
            <Route path="ssh-keys" element={<UserSSHKeysPage />} />
            <Route
              path="api-tokens"
              element={
                <CapabilityRoute cap="api_enabled" fallback="/jabali-panel/dashboard">
                  <UserAPITokensPage />
                </CapabilityRoute>
              }
            />
            <Route path="api-docs" element={<APIDocsPage />} />
            <Route path="cron" element={<UserCronList />} />
            <Route path="backups" element={<UserBackupsPage />} />
            <Route path="mail" element={<Navigate to="/jabali-panel/mail/mailboxes" replace />} />
            <Route
              path="mail/:tab"
              element={
                <CapabilityRoute cap="mail_enabled" fallback="/jabali-panel/dashboard">
                  <MailTabsPage />
                </CapabilityRoute>
              }
            />
            <Route path="mailboxes" element={<Navigate to="/jabali-panel/mail/mailboxes" replace />} />
          </Route>

          {/* ---------------- public ---------------- */}
          <Route
            path="/login"
            element={
              <PublicOnly>
                <LoginPage />
              </PublicOnly>
            }
          />
          {/* /settings is the ui_url Kratos uses for the post-login
              settings flow (kratos.yml.tmpl). We don't want a separate
              page — the Kratos form renders inline on My profile —
              so this route just preserves ?flow=<id> while bouncing
              to the user-shell profile path. */}
          <Route path="/settings" element={<KratosSettingsRedirect />} />

          {/* landing / catch-all */}
          <Route path="/" element={<LandingRedirect />} />
          <Route path="*" element={<LandingRedirect />} />
        </Routes>
        </AntdApp>
      </ConfigProvider>
    </BrowserRouter>
  );
};

const App = () => (
  <QueryClientProvider client={queryClient}>
    <AuthProvider>
      <ThemeModeProvider>
        <ThemedApp />
      </ThemeModeProvider>
    </AuthProvider>
  </QueryClientProvider>
);

export default App;

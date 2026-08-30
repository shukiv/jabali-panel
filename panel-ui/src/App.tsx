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
import { lazy, Suspense, useEffect } from "react";
import { RouteErrorBoundary } from "./components/RouteErrorBoundary";
import { FeedbackBridge } from "./lib/feedback";

import { AuthProvider, useAuth } from "./auth/AuthContext";
import { PublicOnly } from "./auth/PublicOnly";
import { RequireAdmin } from "./auth/RequireAdmin";
import { RequireUser } from "./auth/RequireUser";
import useMuiTheme from "./muiTheme";
import { useTranslation } from "react-i18next";
import { antdLocale, isRTL } from "./i18n";
import { queryClient } from "./query";
import { AdminLayout } from "./shells/AdminLayout";
import { UserLayout } from "./shells/UserLayout";
import { LandingRedirect } from "./shells/LandingRedirect";
import { RecoveryRedeem } from "./shells/RecoveryRedeem";
import { ThemeModeProvider, useThemeMode } from "./theme/ThemeModeContext";
import { useApplyBrandingToTitle, useBranding } from "./hooks/useBranding";
import { PANEL_COLORS } from "./lib/panelColors";
import { CapabilityRoute } from "./components/CapabilityRoute";
import { LoginPage } from "./pages/Login";

// JAB-145: route-level code splitting — each page is its own lazy chunk
// so the initial bundle no longer eagerly pulls all ~55 pages. Layouts,
// guards, and the public LoginPage stay eager (needed on first paint).
// JAB-159 phase 2: demo-only UI, gated behind VITE_DEMO. The ternary
// condition is a build-time constant (Vite define), so a non-demo build
// dead-code-eliminates the import() and tree-shakes DemoBanner +
// useServiceInfo out of the production bundle.
const DemoBanner =
  import.meta.env.VITE_DEMO === "1"
    ? lazy(() => import("./components/DemoBanner").then((m) => ({ default: m.DemoBanner })))
    : null;

const Dashboard = lazy(() => import("./shells/admin/Dashboard").then((m) => ({ default: m.Dashboard })));
const UserList = lazy(() => import("./shells/admin/users/UserList").then((m) => ({ default: m.UserList })));
const AdminUserOverview = lazy(() => import("./shells/admin/users/AdminUserOverview").then((m) => ({ default: m.AdminUserOverview })));
const AdminIPList = lazy(() => import("./shells/admin/ips/AdminIPList").then((m) => ({ default: m.AdminIPList })));
const AdminMailPage = lazy(() => import("./shells/admin/mail/AdminMailPage").then((m) => ({ default: m.AdminMailPage })));
const AdminAuditList = lazy(() => import("./shells/admin/audit/AdminAuditList").then((m) => ({ default: m.AdminAuditList })));
const NotificationsTabsPage = lazy(() => import("./shells/admin/notifications/NotificationsTabsPage").then((m) => ({ default: m.NotificationsTabsPage })));
const AdminSecurityPage = lazy(() => import("./shells/admin/security/AdminSecurityPage").then((m) => ({ default: m.AdminSecurityPage })));
const ServerStatusPage = lazy(() => import("./shells/admin/server-status/ServerStatusPage").then((m) => ({ default: m.ServerStatusPage })));
const CacheOverviewPage = lazy(() => import("./shells/admin/cache/CacheOverviewPage").then((m) => ({ default: m.CacheOverviewPage })));
const MailDeliverabilityPage = lazy(() => import("./shells/admin/mail/MailDeliverabilityPage").then((m) => ({ default: m.MailDeliverabilityPage })));
const MailThrottlesPage = lazy(() => import("./shells/admin/mail/MailThrottlesPage").then((m) => ({ default: m.MailThrottlesPage })));
const SystemUpdatesPage = lazy(() => import("./shells/admin/updates/SystemUpdatesPage").then((m) => ({ default: m.SystemUpdatesPage })));
const SupportPage = lazy(() => import("./shells/admin/support/SupportPage").then((m) => ({ default: m.SupportPage })));
const AdminAutomationTokensPage = lazy(() => import("./shells/admin/automation/AdminAutomationTokensPage").then((m) => ({ default: m.AdminAutomationTokensPage })));
const AdminMigrationsPage = lazy(() => import("./shells/admin/migrations/AdminMigrationsPage").then((m) => ({ default: m.AdminMigrationsPage })));
const AdminMigrationDetailPage = lazy(() => import("./shells/admin/migrations/AdminMigrationDetailPage").then((m) => ({ default: m.AdminMigrationDetailPage })));
const AdminBackupsPage = lazy(() => import("./shells/admin/backups/AdminBackupsPage").then((m) => ({ default: m.AdminBackupsPage })));
const PackageCreate = lazy(() => import("./shells/admin/packages/PackageCreate").then((m) => ({ default: m.PackageCreate })));
const PackageEdit = lazy(() => import("./shells/admin/packages/PackageEdit").then((m) => ({ default: m.PackageEdit })));
const PackageList = lazy(() => import("./shells/admin/packages/PackageList").then((m) => ({ default: m.PackageList })));
const DomainCreate = lazy(() => import("./shells/admin/domains/DomainCreate").then((m) => ({ default: m.DomainCreate })));
const DomainEdit = lazy(() => import("./shells/admin/domains/DomainEdit").then((m) => ({ default: m.DomainEdit })));
const DomainList = lazy(() => import("./shells/admin/domains/DomainList").then((m) => ({ default: m.DomainList })));
const ServerSettingsPage = lazy(() => import("./shells/admin/settings/ServerSettingsPage").then((m) => ({ default: m.ServerSettingsPage })));
const AdminTerminal = lazy(() => import("./shells/admin/terminal/AdminTerminal").then((m) => ({ default: m.AdminTerminal })));
const MyProfile = lazy(() => import("./shells/user/MyProfile").then((m) => ({ default: m.MyProfile })));
const UserDashboard = lazy(() => import("./shells/user/UserDashboard").then((m) => ({ default: m.UserDashboard })));
const FileManagerPage = lazy(() => import("./shells/user/files/FileManagerPage").then((m) => ({ default: m.FileManagerPage })));
const AdminFileManagerPage = lazy(() => import("./shells/admin/files/AdminFileManagerPage").then((m) => ({ default: m.AdminFileManagerPage })));
const UserDomainList = lazy(() => import("./shells/user/domains/UserDomainList").then((m) => ({ default: m.UserDomainList })));
const UserDatabasesPage = lazy(() => import("./shells/user/databases/UserDatabasesPage").then((m) => ({ default: m.UserDatabasesPage })));
const DNSRecordsPage = lazy(() => import("./shells/dns/DNSRecordsPage").then((m) => ({ default: m.DNSRecordsPage })));
const DNSZonesOverviewPage = lazy(() => import("./shells/admin/dns/DNSZonesOverviewPage").then((m) => ({ default: m.DNSZonesOverviewPage })));
const UserDNSZonesOverviewPage = lazy(() => import("./shells/user/dns/UserDNSZonesOverviewPage").then((m) => ({ default: m.UserDNSZonesOverviewPage })));
const SSLManagerPage = lazy(() => import("./shells/admin/ssl/SSLManagerPage").then((m) => ({ default: m.SSLManagerPage })));
const UserSSLManagerPage = lazy(() => import("./shells/user/ssl/UserSSLManagerPage").then((m) => ({ default: m.UserSSLManagerPage })));
const UserPHPSettingsPage = lazy(() => import("./shells/user/php-settings/UserPHPSettingsPage").then((m) => ({ default: m.UserPHPSettingsPage })));
const UserApplicationList = lazy(() => import("./shells/user/applications/UserApplicationList").then((m) => ({ default: m.UserApplicationList })));
const DiskUsagePage = lazy(() => import("./shells/user/disk-usage/DiskUsagePage").then((m) => ({ default: m.DiskUsagePage })));
const PythonAppsPage = lazy(() => import("./shells/user/python-apps/PythonAppsPage").then((m) => ({ default: m.PythonAppsPage })));
const UserDockerAppsPage = lazy(() => import("./shells/user/docker-apps/UserDockerAppsPage").then((m) => ({ default: m.UserDockerAppsPage })));
const UserBackupsPage = lazy(() => import("./shells/user/backups/UserBackupsPage").then((m) => ({ default: m.UserBackupsPage })));
const UserLogsPage = lazy(() => import("./shells/user/logs/UserLogsPage").then((m) => ({ default: m.UserLogsPage })));
const UserSSHKeysPage = lazy(() => import("./shells/user/ssh-keys/UserSSHKeysPage").then((m) => ({ default: m.UserSSHKeysPage })));
const UserFtpAccountsPage = lazy(() => import("./shells/user/ftp-accounts/UserFtpAccountsPage").then((m) => ({ default: m.UserFtpAccountsPage })));
const UserAPITokensPage = lazy(() => import("./shells/user/api-tokens/UserAPITokensPage").then((m) => ({ default: m.UserAPITokensPage })));
const MyNotificationsPage = lazy(() => import("./shells/user/notifications/MyNotificationsPage").then((m) => ({ default: m.MyNotificationsPage })));
const APIDocsPage = lazy(() => import("./shells/shared/APIDocsPage").then((m) => ({ default: m.APIDocsPage })));
const UserCronList = lazy(() => import("./shells/user/cron/UserCronList").then((m) => ({ default: m.UserCronList })));
const AdminCronList = lazy(() => import("./shells/admin/cron/AdminCronList").then((m) => ({ default: m.AdminCronList })));
const MailTabsPage = lazy(() => import("./shells/user/mail/MailTabsPage").then((m) => ({ default: m.MailTabsPage })));
// GH #1387 foundation slice: per-domain Mail Domains summary list.
const MailDomainsPage = lazy(() => import("./shells/user/mail/MailDomainsPage").then((m) => ({ default: m.MailDomainsPage })));
const AdminApplicationList = lazy(() => import("./shells/admin/applications/AdminApplicationList").then((m) => ({ default: m.AdminApplicationList })));
const AdminDockerAppsPage = lazy(() => import("./shells/admin/docker-apps/AdminDockerAppsPage").then((m) => ({ default: m.AdminDockerAppsPage })));
const LogsPage = lazy(() => import("./shells/admin/logs/LogsPage").then((m) => ({ default: m.LogsPage })));
const PHPVersionsPage = lazy(() => import("./shells/admin/php/PHPVersionsPage").then((m) => ({ default: m.PHPVersionsPage })));
const PHPPoolEdit = lazy(() => import("./shells/admin/php-pools/PHPPoolEdit").then((m) => ({ default: m.PHPPoolEdit })));


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


const BrandingTitleApplier = () => {
  useApplyBrandingToTitle();
  return null;
};

const PANEL_FONT_PX: Record<string, number> = { small: 13, medium: 15, large: 17 };

const ThemedApp = () => {
  const { mode } = useThemeMode();
  // Re-render the ConfigProvider when the language changes so AntD picks up
  // the new locale strings + text direction (RTL for he/ar).
  const { i18n } = useTranslation();
  const lng = i18n.resolvedLanguage ?? "en";
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
        locale={antdLocale(lng)}
        direction={isRTL(lng) ? "rtl" : "ltr"}
        renderEmpty={() => <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />}
      >
        <AntdApp
          // GH #970: one house style for every toast — slim, top-centre,
          // capped so bursts don't stack. message has no placement (always
          // top-centre); notification is pulled to `top` to sit with it. These
          // config the App-scoped (hook + FeedbackBridge) instances; the static
          // fallbacks are matched in main.tsx.
          message={{ top: 24, duration: 3, maxCount: 3 }}
          notification={{ placement: "top", top: 24, duration: 4.5, maxCount: 3 }}
        >
        {/* Binds the theme-aware message/notification/modal instances into
            lib/feedback so any call site (incl. non-component code) is
            consistent + themed (GH #970). Renders no DOM. */}
        <FeedbackBridge />
        {/* JAB-159 phase 2: demo UI is compiled out of production bundles.
            VITE_DEMO is statically replaced by Vite; when it is not "1" the
            dynamic import above is tree-shaken, dropping DemoBanner +
            useServiceInfo from the prod SPA entirely. */}
        {DemoBanner ? (
          <Suspense fallback={null}>
            <DemoBanner />
          </Suspense>
        ) : null}
        <BrandingTitleApplier />
        {/* Outside Suspense on purpose: Suspense handles a PENDING lazy
            import, never a rejected one. A chunk that 404s across a deploy
            throws past it, and without this boundary React unmounts the tree
            and the user gets a blank screen. */}
        <RouteErrorBoundary>
        <Suspense
          fallback={
            <div
              style={{
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                minHeight: "60vh",
              }}
            >
              <Spin size="large" />
            </div>
          }
        >
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
            <Route path="admin-files" element={<AdminFileManagerPage />} />
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
              path="ftp-accounts"
              element={
                <CapabilityRoute cap="ftp_accounts_enabled" fallback="/jabali-panel/dashboard">
                  <UserFtpAccountsPage />
                </CapabilityRoute>
              }
            />
            <Route
              path="api-tokens"
              element={
                <CapabilityRoute cap="api_enabled" fallback="/jabali-panel/dashboard">
                  <UserAPITokensPage />
                </CapabilityRoute>
              }
            />
            <Route path="api-docs" element={<APIDocsPage />} />
            <Route path="notifications" element={<MyNotificationsPage />} />
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
            {/* GH #1387 foundation slice — reachable route; sidebar/menu wiring
                is left to the operator's mail restructure (Mail → Mail Domains). */}
            <Route
              path="mail-domains"
              element={
                <CapabilityRoute cap="mail_enabled" fallback="/jabali-panel/dashboard">
                  <MailDomainsPage />
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

          {/* JAB-232 (ADR-0165 addendum): billing-SSO login-link redemption.
              Public — runs before a session exists; reads ?flow=&code= and
              submits the Kratos code recovery, then lands the user authenticated. */}
          <Route path="/recovery" element={<RecoveryRedeem />} />

          {/* landing / catch-all */}
          <Route path="/" element={<LandingRedirect />} />
          <Route path="*" element={<LandingRedirect />} />
        </Routes>
        </Suspense>
        </RouteErrorBoundary>
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

// nav.ts — single source of truth for sidebar menu items.
//
// Replaces Refine's `resources={[...]}` config. Each shell owns its
// own list so an admin page can never accidentally leak into the user
// menu. The shape is small on purpose — anything page-specific
// (search behavior, row actions) lives in the page itself, not here.
//
// `path` is the absolute URL the item navigates to. `match` optionally
// lists longer paths that should still highlight this entry — useful
// when a nested route (e.g. /jabali-admin/users/create) should keep
// the "Users" row active.
import type { ReactNode } from "react";

import {
  AppstoreOutlined,
  BellOutlined,
  CalendarCheckOutlined,
  ChartBarOutlined,
  ThunderboltOutlined,
  CloudServerOutlined,
  CodeOutlined,
  SquareTerminalOutlined,
  DownloadOutlined,
  FileTextOutlined,
  LifeBuoyOutlined,
  DatabaseOutlined,
  EthernetPortOutlined,
  FolderOutlined,
  GlobalOutlined,
  HddOutlined,
  HomeOutlined,
  KeyOutlined,
  ApiOutlined,
  MailOutlined,
  PackageOpenOutlined,
  SafetyOutlined,
  SaveOutlined,
  ServerOutlined,
  SettingOutlined,
  ShieldCheckOutlined,
  SwapOutlined,
  TeamOutlined,
  ContainerOutlined
} from "@icons";
import { createElement } from "react";
import type { ComponentType } from "react";

export type NavItem = {
  key: string;
  /** i18n key (see src/locales/en/common.json) — translate at render time. */
  label: string;
  icon: ReactNode;
  path: string;
  // i18n key for the hover tooltip shown beside the sidebar item
  // (right-placed) explaining what the tab does.
  description?: string;
  // matchPatterns lets an item claim deeper sub-paths it doesn't own
  // by `path` startsWith. Regex tested against the full pathname; if
  // any matches, the item is selected even when another item's path
  // would be a longer prefix. Use for nested routes like
  // /jabali-panel/domains/:id/dns that logically belong to a different
  // sidebar entry than the one /jabali-panel/domains owns.
  matchPatterns?: RegExp[];
};

// All sidebar icons render a shade larger than AntD's default (14px
// inherited from fontSize). 20px reads comfortably without crowding the
// label and keeps the collapsed-sider footprint tidy. Change here and
// every entry picks it up.
const NAV_ICON_SIZE = 20;

// Tailwind gray-500 (#6b7280) keeps the inactive icon row muted while
// the AntD-active item still gets its brand colour via the Menu's
// itemSelectedColor theme token (which overrides this inline color
// only for the selected key — `currentColor` inheritance kicks in
// on selection).
const NAV_ICON_COLOR = "#6b7280";

const navIcon = (Icon: ComponentType<{ style?: React.CSSProperties }>) =>
  createElement(Icon, { style: { fontSize: NAV_ICON_SIZE, color: NAV_ICON_COLOR } });

export const adminNav: NavItem[] = [
  {
    key: "dashboard",
    label: "nav.admin.dashboard",
    description: "nav.admin.dashboard_desc",
    icon: navIcon(HomeOutlined),
    path: "/jabali-admin/dashboard",
  },
  {
    key: "users",
    label: "nav.admin.users",
    // JAB-126: Sessions moved into the Users page as a tab
    // (/jabali-admin/users?tab=sessions), so this row stays active while
    // viewing sessions and the sidebar loses the standalone Sessions entry.
    description: "nav.admin.users_desc",
    icon: navIcon(TeamOutlined),
    path: "/jabali-admin/users",
  },
  {
    key: "domains",
    label: "nav.admin.domains",
    description: "nav.admin.domains_desc",
    icon: navIcon(GlobalOutlined),
    path: "/jabali-admin/domains",
  },
  {
    key: "mail",
    label: "nav.admin.mail",
    description: "nav.admin.mail_desc",
    icon: navIcon(MailOutlined),
    path: "/jabali-admin/mail",
  },
  {
    key: "packages",
    label: "nav.admin.packages",
    description: "nav.admin.packages_desc",
    icon: navIcon(PackageOpenOutlined),
    path: "/jabali-admin/packages",
  },
  {
    key: "ssl",
    label: "nav.admin.ssl",
    description: "nav.admin.ssl_desc",
    icon: navIcon(ShieldCheckOutlined),
    path: "/jabali-admin/ssl",
  },
  {
    key: "applications",
    label: "nav.admin.applications",
    description: "nav.admin.applications_desc",
    icon: navIcon(AppstoreOutlined),
    path: "/jabali-admin/applications",
  },
  {
    key: "docker-apps",
    label: "nav.admin.docker-apps",
    description: "nav.admin.docker-apps_desc",
    icon: navIcon(ContainerOutlined),
    path: "/jabali-admin/docker-apps",
  },
  {
    key: "settings",
    label: "nav.admin.settings",
    description: "nav.admin.settings_desc",
    icon: navIcon(SettingOutlined),
    path: "/jabali-admin/settings",
  },
  {
    key: "server-status",
    label: "nav.admin.server-status",
    description: "nav.admin.server-status_desc",
    icon: navIcon(ChartBarOutlined),
    path: "/jabali-admin/server-status",
  },
  {
    key: "cache",
    label: "nav.admin.cache",
    description: "nav.admin.cache_desc",
    icon: navIcon(ThunderboltOutlined),
    path: "/jabali-admin/cache",
  },
  {
    key: "security",
    label: "nav.admin.security",
    description: "nav.admin.security_desc",
    icon: navIcon(SafetyOutlined),
    path: "/jabali-admin/security",
  },
  {
    key: "backups",
    label: "nav.admin.backups",
    description: "nav.admin.backups_desc",
    icon: navIcon(SaveOutlined),
    path: "/jabali-admin/backups",
  },
  {
    key: "php-pools",
    label: "nav.admin.php-pools",
    description: "nav.admin.php-pools_desc",
    icon: navIcon(CodeOutlined),
    path: "/jabali-admin/php-pools",
  },
  {
    key: "dns",
    label: "nav.admin.dns",
    description: "nav.admin.dns_desc",
    icon: navIcon(ServerOutlined),
    path: "/jabali-admin/dns",
    matchPatterns: [/^\/jabali-admin\/domains\/[^/]+\/dns(?:\/|$)/],
  },
  {
    key: "ips",
    label: "nav.admin.ips",
    description: "nav.admin.ips_desc",
    icon: navIcon(EthernetPortOutlined),
    path: "/jabali-admin/ips",
  },
  {
    key: "notifications",
    label: "nav.admin.notifications",
    description: "nav.admin.notifications_desc",
    icon: navIcon(BellOutlined),
    path: "/jabali-admin/notifications/channels",
  },
  {
    key: "updates",
    label: "nav.admin.updates",
    description: "nav.admin.updates_desc",
    icon: navIcon(DownloadOutlined),
    path: "/jabali-admin/updates",
  },
  {
    key: "automation",
    label: "nav.admin.automation",
    description: "nav.admin.automation_desc",
    icon: navIcon(KeyOutlined),
    path: "/jabali-admin/automation",
  },
  {
    key: "api-tokens",
    label: "nav.admin.api-tokens",
    description: "nav.admin.api-tokens_desc",
    icon: navIcon(ApiOutlined),
    path: "/jabali-admin/api-tokens",
  },
  {
    key: "migrations",
    label: "nav.admin.migrations",
    description: "nav.admin.migrations_desc",
    icon: navIcon(SwapOutlined),
    path: "/jabali-admin/migrations",
  },
  {
    key: "terminal",
    label: "nav.admin.terminal",
    description: "nav.admin.terminal_desc",
    icon: navIcon(SquareTerminalOutlined),
    path: "/jabali-admin/terminal",
  },
  {
    key: "logs",
    label: "nav.admin.logs",
    description: "nav.admin.logs_desc",
    icon: navIcon(FileTextOutlined),
    path: "/jabali-admin/logs",
  },
  {
    key: "cron",
    label: "nav.admin.cron",
    description: "nav.admin.cron_desc",
    icon: navIcon(CalendarCheckOutlined),
    path: "/jabali-admin/cron",
  },
  {
    key: "support",
    label: "nav.admin.support",
    description: "nav.admin.support_desc",
    icon: navIcon(LifeBuoyOutlined),
    path: "/jabali-admin/support",
  },
];

export const userNav: NavItem[] = [
  {
    key: "dashboard",
    label: "nav.user.dashboard",
    icon: navIcon(HomeOutlined),
    path: "/jabali-panel/dashboard",
  },
  {
    key: "domains",
    label: "nav.user.domains",
    icon: navIcon(GlobalOutlined),
    path: "/jabali-panel/domains",
  },
  {
    key: "mail",
    label: "nav.user.mail",
    icon: navIcon(MailOutlined),
    path: "/jabali-panel/mail/mailboxes",
    // The link targets the default subtab, but the item owns every mail
    // subtab (groups/forwarders/…) — keep it highlighted across all of them
    // (GH #314). matchPatterns win over the longest-path-prefix match.
    matchPatterns: [/^\/jabali-panel\/mail(?:\/|$)/],
  },
  {
    key: "applications",
    label: "nav.user.applications",
    icon: navIcon(AppstoreOutlined),
    path: "/jabali-panel/applications",
  },
  {
    key: "python-apps",
    label: "nav.user.python-apps",
    icon: navIcon(CodeOutlined),
    path: "/jabali-panel/python-apps",
  },
  {
    key: "docker-apps",
    label: "nav.user.docker-apps",
    icon: navIcon(AppstoreOutlined),
    path: "/jabali-panel/docker-apps",
  },
  {
    key: "databases",
    label: "nav.user.databases",
    icon: navIcon(DatabaseOutlined),
    path: "/jabali-panel/databases",
  },
  {
    key: "disk-usage",
    label: "nav.user.disk-usage",
    icon: navIcon(HddOutlined),
    path: "/jabali-panel/disk-usage",
  },
  {
    key: "files",
    label: "nav.user.files",
    icon: navIcon(FolderOutlined),
    path: "/jabali-panel/files",
  },
  {
    key: "logs",
    label: "nav.user.logs",
    icon: navIcon(FileTextOutlined),
    path: "/jabali-panel/logs",
  },
  {
    key: "ssh-keys",
    label: "nav.user.ssh-keys",
    icon: navIcon(KeyOutlined),
    path: "/jabali-panel/ssh-keys",
  },
  {
    key: "ftp-accounts",
    label: "nav.user.ftp-accounts",
    icon: navIcon(CloudServerOutlined),
    path: "/jabali-panel/ftp-accounts",
  },
  {
    key: "api-tokens",
    label: "nav.user.api-tokens",
    icon: navIcon(ApiOutlined),
    path: "/jabali-panel/api-tokens",
  },
  {
    key: "notifications",
    label: "nav.user.notifications",
    icon: navIcon(BellOutlined),
    path: "/jabali-panel/notifications",
  },
  {
    key: "dns",
    label: "nav.user.dns",
    icon: navIcon(CloudServerOutlined),
    path: "/jabali-panel/dns",
    matchPatterns: [/^\/jabali-panel\/domains\/[^/]+\/dns(?:\/|$)/],
  },
  {
    key: "ssl",
    label: "nav.user.ssl",
    icon: navIcon(ShieldCheckOutlined),
    path: "/jabali-panel/ssl",
  },
  {
    key: "php-settings",
    label: "nav.user.php-settings",
    icon: navIcon(CodeOutlined),
    path: "/jabali-panel/php-settings",
  },
  {
    key: "cron",
    label: "nav.user.cron",
    icon: navIcon(CalendarCheckOutlined),
    path: "/jabali-panel/cron",
  },
  {
    key: "backups",
    label: "nav.user.backups",
    icon: navIcon(SaveOutlined),
    path: "/jabali-panel/backups",
  },
];

/**
 * Pick the best-matching menu entry for the current pathname using
 * longest-prefix match. Ensures /jabali-admin/users/create still
 * highlights "Users".
 */
export function selectedNavKey(
  items: NavItem[],
  pathname: string,
): string | undefined {
  // matchPatterns win over startsWith — they're explicit overrides for
  // nested routes that semantically belong to a different sidebar entry
  // than the longest path prefix would pick.
  const byPattern = items.find((item) =>
    item.matchPatterns?.some((re) => re.test(pathname)),
  );
  if (byPattern) return byPattern.key;
  return [...items]
    .sort((a, b) => b.path.length - a.path.length)
    .find((item) => pathname.startsWith(item.path))?.key;
}

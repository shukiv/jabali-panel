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
  label: string;
  icon: ReactNode;
  path: string;
  // description is the hover tooltip shown beside the sidebar item
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
    label: "Dashboard",
    description: "Server overview and quick stats",
    icon: navIcon(HomeOutlined),
    path: "/jabali-admin/dashboard",
  },
  {
    key: "users",
    label: "Users",
    // JAB-126: Sessions moved into the Users page as a tab
    // (/jabali-admin/users?tab=sessions), so this row stays active while
    // viewing sessions and the sidebar loses the standalone Sessions entry.
    description: "Manage panel users, sessions, and impersonation",
    icon: navIcon(TeamOutlined),
    path: "/jabali-admin/users",
  },
  {
    key: "domains",
    label: "Domains",
    description: "All domains across every user",
    icon: navIcon(GlobalOutlined),
    path: "/jabali-admin/domains",
  },
  {
    key: "mail",
    label: "Mail",
    description: "Mailboxes, forwarders, and webmail",
    icon: navIcon(MailOutlined),
    path: "/jabali-admin/mail",
  },
  {
    key: "packages",
    label: "Hosting Packages",
    description: "Hosting package plans and limits",
    icon: navIcon(PackageOpenOutlined),
    path: "/jabali-admin/packages",
  },
  {
    key: "ssl",
    label: "SSL Manager",
    description: "SSL certificates and issuance",
    icon: navIcon(ShieldCheckOutlined),
    path: "/jabali-admin/ssl",
  },
  {
    key: "applications",
    label: "Applications",
    description: "One-click app installs (WordPress and more)",
    icon: navIcon(AppstoreOutlined),
    path: "/jabali-admin/applications",
  },
  {
    key: "docker-apps",
    label: "Docker Apps",
    description: "Docker app marketplace",
    icon: navIcon(ContainerOutlined),
    path: "/jabali-admin/docker-apps",
  },
  {
    key: "settings",
    label: "Server Settings",
    description: "Server-wide settings and optional modules",
    icon: navIcon(SettingOutlined),
    path: "/jabali-admin/settings",
  },
  {
    key: "server-status",
    label: "Server Status",
    description: "Live services, resources, and health",
    icon: navIcon(ChartBarOutlined),
    path: "/jabali-admin/server-status",
  },
  {
    key: "cache",
    label: "Cache",
    description: "Redis and WordPress cache tools",
    icon: navIcon(ThunderboltOutlined),
    path: "/jabali-admin/cache",
  },
  {
    key: "security",
    label: "Security",
    description: "CrowdSec, malware scanning, and firewall",
    icon: navIcon(SafetyOutlined),
    path: "/jabali-admin/security",
  },
  {
    key: "backups",
    label: "Backups",
    description: "Backup destinations and schedules",
    icon: navIcon(SaveOutlined),
    path: "/jabali-admin/backups",
  },
  {
    key: "php-pools",
    label: "PHP Manager",
    description: "PHP versions and FPM pools",
    icon: navIcon(CodeOutlined),
    path: "/jabali-admin/php-pools",
  },
  {
    key: "dns",
    label: "DNS Zones",
    description: "Authoritative DNS zones and records",
    icon: navIcon(ServerOutlined),
    path: "/jabali-admin/dns",
    matchPatterns: [/^\/jabali-admin\/domains\/[^/]+\/dns(?:\/|$)/],
  },
  {
    key: "ips",
    label: "IP Addresses",
    description: "Managed server IP addresses",
    icon: navIcon(EthernetPortOutlined),
    path: "/jabali-admin/ips",
  },
  {
    key: "notifications",
    label: "Notifications",
    description: "Alerts and notification channels",
    icon: navIcon(BellOutlined),
    path: "/jabali-admin/notifications/channels",
  },
  {
    key: "updates",
    label: "Updates",
    description: "Panel and system package updates",
    icon: navIcon(DownloadOutlined),
    path: "/jabali-admin/updates",
  },
  {
    key: "automation",
    label: "Server API Access",
    description: "Server/global API tokens and scopes for automations",
    icon: navIcon(KeyOutlined),
    path: "/jabali-admin/automation",
  },
  {
    key: "api-tokens",
    label: "Personal API Tokens",
    description: "REST API keys",
    icon: navIcon(ApiOutlined),
    path: "/jabali-admin/api-tokens",
  },
  {
    key: "migrations",
    label: "Migrations",
    description: "Import accounts from cPanel, HestiaCP, or DirectAdmin",
    icon: navIcon(SwapOutlined),
    path: "/jabali-admin/migrations",
  },
  {
    key: "terminal",
    label: "Terminal",
    description: "Web shell to the server",
    icon: navIcon(SquareTerminalOutlined),
    path: "/jabali-admin/terminal",
  },
  {
    key: "logs",
    label: "Logs & Statistics",
    description: "Access and error logs plus statistics",
    icon: navIcon(FileTextOutlined),
    path: "/jabali-admin/logs",
  },
  {
    key: "cron",
    label: "Cron",
    description: "Scheduled cron jobs",
    icon: navIcon(CalendarCheckOutlined),
    path: "/jabali-admin/cron",
  },
  {
    key: "support",
    label: "Support",
    description: "Diagnostics and contact support",
    icon: navIcon(LifeBuoyOutlined),
    path: "/jabali-admin/support",
  },
];

export const userNav: NavItem[] = [
  {
    key: "dashboard",
    label: "Dashboard",
    icon: navIcon(HomeOutlined),
    path: "/jabali-panel/dashboard",
  },
  {
    key: "domains",
    label: "Domains",
    icon: navIcon(GlobalOutlined),
    path: "/jabali-panel/domains",
  },
  {
    key: "mail",
    label: "Mail",
    icon: navIcon(MailOutlined),
    path: "/jabali-panel/mail/mailboxes",
    // The link targets the default subtab, but the item owns every mail
    // subtab (groups/forwarders/…) — keep it highlighted across all of them
    // (GH #314). matchPatterns win over the longest-path-prefix match.
    matchPatterns: [/^\/jabali-panel\/mail(?:\/|$)/],
  },
  {
    key: "applications",
    label: "Applications",
    icon: navIcon(AppstoreOutlined),
    path: "/jabali-panel/applications",
  },
  {
    key: "python-apps",
    label: "Python Apps",
    icon: navIcon(CodeOutlined),
    path: "/jabali-panel/python-apps",
  },
  {
    key: "docker-apps",
    label: "Docker Apps",
    icon: navIcon(AppstoreOutlined),
    path: "/jabali-panel/docker-apps",
  },
  {
    key: "databases",
    label: "Databases",
    icon: navIcon(DatabaseOutlined),
    path: "/jabali-panel/databases",
  },
  {
    key: "disk-usage",
    label: "Disk Usage",
    icon: navIcon(HddOutlined),
    path: "/jabali-panel/disk-usage",
  },
  {
    key: "files",
    label: "Files",
    icon: navIcon(FolderOutlined),
    path: "/jabali-panel/files",
  },
  {
    key: "logs",
    label: "Logs & Statistics",
    icon: navIcon(FileTextOutlined),
    path: "/jabali-panel/logs",
  },
  {
    key: "ssh-keys",
    label: "SSH Keys",
    icon: navIcon(KeyOutlined),
    path: "/jabali-panel/ssh-keys",
  },
  {
    key: "api-tokens",
    label: "Personal API Tokens",
    icon: navIcon(ApiOutlined),
    path: "/jabali-panel/api-tokens",
  },
  {
    key: "dns",
    label: "DNS",
    icon: navIcon(CloudServerOutlined),
    path: "/jabali-panel/dns",
    matchPatterns: [/^\/jabali-panel\/domains\/[^/]+\/dns(?:\/|$)/],
  },
  {
    key: "ssl",
    label: "SSL Manager",
    icon: navIcon(ShieldCheckOutlined),
    path: "/jabali-panel/ssl",
  },
  {
    key: "php-settings",
    label: "PHP Settings",
    icon: navIcon(CodeOutlined),
    path: "/jabali-panel/php-settings",
  },
  {
    key: "cron",
    label: "Cron",
    icon: navIcon(CalendarCheckOutlined),
    path: "/jabali-panel/cron",
  },
  {
    key: "backups",
    label: "Backup/Restore",
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

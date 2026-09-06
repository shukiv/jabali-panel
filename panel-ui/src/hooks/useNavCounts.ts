import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

// GH #1478: side-nav badge counts. One aggregate call per side
// (GET /me/nav-counts | GET /admin/nav-counts) rather than a list query per
// nav item.
export type NavCounts = {
  web_domains: number;
  mail_domains: number;
  dns_zones: number;
  databases: number;
  ftp_accounts: number;
  backups: number;
  cron_jobs: number;
};

// navCountForKey maps a nav item's `key` (src/nav.ts) to the matching count
// field, so the layout doesn't hard-code the mapping inline. Returns undefined
// for nav items that don't carry a badge.
export function navCountForKey(counts: NavCounts | undefined, key: string): number | undefined {
  if (!counts) return undefined;
  switch (key) {
    case "domains":
      return counts.web_domains;
    case "mail":
      return counts.mail_domains;
    case "dns":
      return counts.dns_zones;
    case "databases":
      return counts.databases;
    case "ftp-accounts":
      return counts.ftp_accounts;
    case "backups":
      return counts.backups;
    case "cron":
      return counts.cron_jobs;
    default:
      return undefined;
  }
}

export function useNavCounts(scope: "me" | "admin") {
  return useQuery<NavCounts>({
    queryKey: ["nav-counts", scope],
    queryFn: async () => {
      const { data } = await apiClient.get<NavCounts>(`/${scope}/nav-counts`);
      return data;
    },
    // Counts change rarely relative to nav renders; a short stale window keeps
    // the badges live without a request on every page change.
    staleTime: 60_000,
    refetchOnWindowFocus: true,
  });
}

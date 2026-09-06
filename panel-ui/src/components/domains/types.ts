// JAB-300: the canonical domain-inventory contract, shared by the admin
// (`DomainList`) and tenant (`UserDomainList`) domain grids and by the Mail
// Domains screens. Before this it lived in each page file, so the admin mail
// tabs imported the *tenant* page's `Domain` type — the exact "admin imports
// tenant types" smell this module removes. `Domain` is the union of both
// screens' wire shapes: every field that only one audience sends is optional,
// so a row from either endpoint satisfies the type and each screen reads the
// subset it cares about.
import type { ApplicationStatus } from "../../utils/applicationStatus";

// The nested SSL badge the admin list/get handler denormalises onto each row.
// The tenant list sends a flat `ssl_state` string instead; both fold through
// utils/sslState.getSSLTag so the two screens can't drift (JAB-300 SSL matrix,
// shipped in #1494).
export type SSLBadge = {
  status: string;
  issuer?: string | null;
  issued_at?: string | null;
  expires_at?: string | null;
};

// GH #1543: the per-domain One-Click app summary the domains list handler
// denormalises onto each row (docroot-first). Just what the tenant Web Domains
// Application column needs — badge, version, live status, and enough to mint a
// login; the full install record stays on GET /applications. Reuses the shared
// ApplicationStatus vocabulary so the column and the Applications page can't
// drift on status meaning.
export type DomainApplicationSummary = {
  id: string;
  app_type: string;
  version: string | null;
  status: ApplicationStatus;
  last_error?: string;
  subdirectory: string;
};

export type Domain = {
  id: string;
  user_id: string;
  username?: string | null;
  name: string;
  doc_root: string;
  is_enabled: boolean;
  // Admin-only wire fields (the tenant list never sends these).
  is_panel_primary?: boolean;
  is_quota_suspended?: boolean;
  ssl_enabled?: boolean;
  mail_provider?: string;
  m365_onmicrosoft?: string;
  google_dkim?: string;
  ssl?: SSLBadge | null;
  bot_challenge_exempt?: boolean;
  // M24: nullable per-family binding to a managed_ips row. NULL ⇒ use
  // server default. listen_ipv4 / listen_ipv6 are the denormalized
  // {id,address} blob the list/get handler computes server-side.
  listen_ipv4_id?: number | null;
  listen_ipv6_id?: number | null;
  listen_ipv4?: { id: number; address: string } | null;
  listen_ipv6?: { id: number; address: string } | null;
  // Tenant-only wire fields (the admin list never sends these).
  // GH #1175: >0 marks a reverse-proxy domain forwarding to this loopback port.
  reverse_proxy_port?: number;
  ssl_state?: string;
  email_enabled?: boolean;
  // GH #1449: independent services. web_disabled = docroot-less (DNS-only /
  // mail-only); dns_disabled = the panel doesn't host this domain's DNS.
  web_disabled?: boolean;
  dns_disabled?: boolean;
  // GH #1543: this domain's One-Click app installs, denormalised onto the row
  // for the tenant Web Domains Application column. Absent (never null) when the
  // domain has none or the module is unwired; ordered docroot-first.
  applications?: DomainApplicationSummary[];
  // Shared across both audiences.
  bytes_30d?: number;
  temp_url_enabled?: boolean;
  temp_url?: string | null;
  bot_challenge_include?: boolean;
  nginx_custom_directives: string;
  redirect_all_to?: string | null;
  redirect_all_type?: string | null;
  page_redirects?:
    | { source: string; destination: string; type: "301" | "302" | "307" | "308" }[]
    | null;
  index_priority?:
    | "html_first"
    | "php_first"
    | "html_only"
    | "php_only"
    | "full"
    | null;
  created_at: string;
  updated_at: string;
};

// GH #1449: a short badge for a domain that isn't a plain full-service web
// site, so the three row kinds are distinguishable in the tenant list. Returns
// null for an ordinary web domain (the common case — no badge noise).
export const serviceBadge = (
  d: Pick<Domain, "web_disabled" | "dns_disabled" | "email_enabled">,
): { label: string; color: string } | null => {
  if (d.web_disabled) {
    return d.email_enabled
      ? { label: "Mail only", color: "purple" }
      : { label: "DNS only", color: "blue" };
  }
  if (d.dns_disabled) {
    return { label: "External DNS", color: "default" };
  }
  return null;
};

// useServerCapabilities — shared, cached read of the opt-in feature flags the
// panel gates UI on (GH #229 + gap-audit #1). Backed by GET
// /me/server-capabilities. Used by the layouts to filter the sidebar AND by
// CapabilityRoute to gate the routes themselves, so a deep-link to a disabled
// feature redirects instead of rendering a page that immediately 403s.
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export interface ServerCapabilities {
  postgres_enabled: boolean;
  docker_marketplace_enabled: boolean;
  docker_apps_user_enabled: boolean;
  python_apps_enabled: boolean;
  tenant_domain_options_enabled: boolean;
  tenant_docroot_editable: boolean;
  dns_enabled: boolean;
  mail_enabled: boolean;
  security_enabled: boolean;
  quota_enabled: boolean;
  api_enabled: boolean;
  /** GH #515: admin web terminal (root_terminal_enabled). Gates the Terminal nav entry. */
  root_terminal_enabled: boolean;
  /** GH #361: the server's public IPv4, for the dashboard. "" when unset. */
  public_ipv4: string;
  /** GH #361: the server's public IPv6, for the dashboard. "" when unset. */
  public_ipv6: string;
  /** GH #331: this box is a DR standby (read-only replica). Drives the banner. */
  is_standby: boolean;
  /** GH #331: human label for the primary this standby replicates. "" when unset. */
  dr_peer_label: string;
}

export function useServerCapabilities() {
  return useQuery<ServerCapabilities>({
    queryKey: ["server-capabilities"],
    queryFn: async () => {
      const { data } = await apiClient.get<Partial<ServerCapabilities>>("/me/server-capabilities");
      return {
        postgres_enabled: !!data.postgres_enabled,
        docker_marketplace_enabled: !!data.docker_marketplace_enabled,
        docker_apps_user_enabled: !!data.docker_apps_user_enabled,
        python_apps_enabled: !!data.python_apps_enabled,
        tenant_domain_options_enabled: !!data.tenant_domain_options_enabled,
        tenant_docroot_editable: !!data.tenant_docroot_editable,
        dns_enabled: data.dns_enabled !== false,
        mail_enabled: data.mail_enabled !== false,
        security_enabled: data.security_enabled !== false,
        quota_enabled: data.quota_enabled !== false,
        api_enabled: data.api_enabled !== false,
        root_terminal_enabled: !!data.root_terminal_enabled,
        public_ipv4: data.public_ipv4 ?? "",
        public_ipv6: data.public_ipv6 ?? "",
        is_standby: !!data.is_standby,
        dr_peer_label: data.dr_peer_label ?? "",
      };
    },
    staleTime: 60_000,
  });
}

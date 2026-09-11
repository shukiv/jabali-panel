// WebDomainPage — GH #1543 (johnnyq): clicking a domain in the tenant Web
// Domains list opens this dedicated page. Its per-domain actions are organised
// as navigable tabs (like the admin Edit Domain page) rather than a row of
// modal launchers. The tab lives in the URL (:tab), so a tab is linkable and
// the browser Back button walks the tabs.
//
// Tabs here: Overview (facts + the preview-URL / bot-challenge toggles), Logs,
// SSL, DNS (gated on dns_enabled), PHP Settings, Redirects, Index Files, Caching
// and Directory Privacy, plus three tabs gated on the same caps as the old row
// menu — Domain options and Rewrite rules (tenant_domain_options_enabled) and
// Document root (tenant_docroot_editable). The tenant row menu is now just
// Enable/Delete; the DNS records manager (DNSRecordsPanel) renders here embedded
// and standalone on the admin route.
//
// The tab bar stays horizontal on desktop (johnnyq's call over a vertical
// sidebar) but collapses to a Select on narrow screens so it doesn't force
// horizontal scrolling on mobile (lxsdevcode).
import type { ReactNode } from "react";
import { Alert, Button, Card, Grid, Select, Skeleton, Space, Typography } from "antd";
import { GlobalOutlined } from "@icons";
import { useNavigate, useParams } from "react-router";

import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { useOneQuery } from "../../../hooks/useQueries";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";
import type { Domain } from "../../../components/domains/types";
import { DomainCacheSection } from "../../../components/DomainCacheSection";
import { DomainDirectoryPrivacySection } from "../../admin/domains/DomainDirectoryPrivacySection";
import { DomainIndexPanel } from "../../DomainIndexPanel";
import { DomainRedirectsPanel } from "../../DomainRedirectsPanel";
import { DNSRecordsPanel } from "../../dns/DNSRecordsPage";
import { DomainLogsPanel } from "../../../components/logs/DomainLogsPanel";
import { DomainNginxOptionsPanel } from "../../../components/DomainNginxOptionsPanel";
import { TenantNginxRulesPanel } from "../../DomainSettingsButton";
import { DomainDocRootPanel } from "../../../components/domains/DomainDocRootPanel";
import { DomainPHPSettingsPanel } from "../../../components/domains/DomainPHPSettingsPanel";
import { RenameDomainButton } from "../../../components/domains/RenameDomainButton";
import { DomainEnvVarsCard } from "../php-settings/DomainEnvVarsCard";
import { OverviewTab } from "./tabs/OverviewTab";
import { SSLTab } from "./tabs/SSLTab";

const DEFAULT_TAB = "overview";
const LIST_PATH = "/jabali-panel/domains";

export const WebDomainPage = () => {
  const { id, tab } = useParams<{ id: string; tab?: string }>();
  const navigate = useNavigate();

  const domainQ = useOneQuery<Domain>({ resource: "domains", id });
  const { data: caps } = useServerCapabilities();
  // Horizontal tabs stay on desktop; on a narrow screen the strip collapses to
  // a Select so the tabs don't force horizontal scrolling (GH #1543). `md ===
  // false` (never just falsy) keeps SSR / first paint / jsdom on the desktop
  // strip — an unknown breakpoint must not flip to the mobile control.
  const screens = Grid.useBreakpoint();
  const mobile = screens.md === false;

  // The shell already renders ONE breadcrumb (RouteBreadcrumb, GH #455). Override
  // it with the entity trail so the last crumb is the domain name, not the raw
  // :id — same approach as MailDomainPage (GH #1387).
  const domainName = domainQ.data?.name;
  useSetBreadcrumbs(
    domainName
      ? [
          { title: "Dashboard", href: "/jabali-panel/dashboard" },
          { title: "Web Domains", href: LIST_PATH },
          { title: domainName },
        ]
      : null,
  );

  const back = () => navigate(LIST_PATH);

  if (domainQ.isLoading) {
    return (
      <div style={{ padding: 20 }}>
        <Skeleton active />
      </div>
    );
  }
  if (domainQ.isError || !domainQ.data) {
    // Owner-scoped GET /domains/:id returns 403/404 for a domain the caller does
    // not own — surface it as an error, never a blank scoped view.
    return (
      <div style={{ padding: 20 }}>
        <Alert
          type="error"
          showIcon
          message="Domain not available"
          description="This domain doesn't exist or you don't have access to it."
          action={<Button onClick={back}>Back to Web Domains</Button>}
        />
      </div>
    );
  }
  const domain = domainQ.data;

  // The cap-gated tabs mirror the row menu exactly: a tenant without the cap
  // sees neither the menu item nor the tab (never a disabled stub).
  const optionsOn = caps?.tenant_domain_options_enabled === true;
  const docrootOn = caps?.tenant_docroot_editable === true;
  // GH #1624: with the cap off the two gated tabs are hidden (never a disabled
  // stub — see below), so a tenant has no way to learn the feature exists. Show
  // a small note on the Overview pane inviting them to ask the admin to enable
  // it. Gate on an explicit `false` (not `undefined`) so it doesn't flash while
  // capabilities are still loading.
  const showOptionsHint = caps?.tenant_domain_options_enabled === false;
  // DNS renders here as a tab (GH #1543). Gate on the same dns_enabled signal
  // the sidebar and the old row-menu item used — default-on while caps load.
  // The tenant DNS Zones overview page links straight into this tab to manage a
  // zone's records, so this is the tenant's per-domain DNS records manager.
  const dnsOn = caps?.dns_enabled !== false;

  const tabs: { key: string; label: string; node: ReactNode }[] = [
    {
      key: "overview",
      label: "Overview",
      node: (
        <Space direction="vertical" size="large" style={{ width: "100%" }}>
          <OverviewTab domain={domain} />
          {showOptionsHint && (
            <Alert
              type="info"
              showIcon
              message="More domain controls are available on request"
              description="When your administrator enables tenant domain options for this server, you get two extra tabs on your domains: a curated set of safe nginx options (max upload size, HSTS, security headers, gzip) and a limited rewrite / custom-response-header rule builder. Raw nginx directives stay admin-only."
            />
          )}
        </Space>
      ),
    },
    // Logs is a read-only diagnostic view — the most-checked thing after
    // "is the site up" — so it sits second, before the editors. It exists for
    // every web domain, so it is not cap-gated.
    { key: "logs", label: "Logs", node: <DomainLogsPanel domainId={domain.id} /> },
    // SSL — this domain's certificate: status / expiry, view the issued cert,
    // renew or retry issuance (GH #1543, lxsdevcode). Certificate mode is shown
    // read-only; switching mode is an admin action.
    { key: "ssl", label: "SSL", node: <SSLTab domain={domain} /> },
    ...(dnsOn
      ? [{ key: "dns", label: "DNS", node: <DNSRecordsPanel domainId={domain.id} embedded /> }]
      : []),
    // PHP Settings — this domain's PHP version + php.ini limit overrides and its
    // env vars. The account/pool-level PHP tabs (Performance, OPcache,
    // Extensions, Xdebug, CLI/Composer) are per-version-pool and stay on the
    // standalone PHP Settings page. GH #1543.
    {
      key: "php-settings",
      label: "PHP Settings",
      node: (
        <Space direction="vertical" size="large" style={{ width: "100%" }}>
          <DomainPHPSettingsPanel domainId={domain.id} />
          <DomainEnvVarsCard domainId={domain.id} />
        </Space>
      ),
    },
    { key: "redirects", label: "Redirects", node: <DomainRedirectsPanel domain={domain} /> },
    { key: "index", label: "Index Files", node: <DomainIndexPanel domain={domain} /> },
    { key: "caching", label: "Caching", node: <DomainCacheSection domainId={domain.id} /> },
    {
      key: "directory-privacy",
      label: "Directory Privacy",
      node: <DomainDirectoryPrivacySection domainId={domain.id} domainName={domain.name} />,
    },
    ...(optionsOn
      ? [
          {
            key: "domain-options",
            label: "Domain options",
            node: <DomainNginxOptionsPanel domainId={domain.id} />,
          },
          {
            key: "rewrite-rules",
            label: "Rewrite rules",
            node: <TenantNginxRulesPanel domain={domain} />,
          },
        ]
      : []),
    ...(docrootOn
      ? [
          {
            key: "document-root",
            label: "Document root",
            node: (
              <DomainDocRootPanel
                domainId={domain.id}
                domainName={domain.name}
                currentDocRoot={domain.doc_root}
              />
            ),
          },
        ]
      : []),
  ];

  const activeKey = tabs.some((tdef) => tdef.key === tab) ? (tab as string) : DEFAULT_TAB;
  const active = tabs.find((tdef) => tdef.key === activeKey) ?? tabs[0];

  return (
    <div style={{ padding: "20px" }}>
      <Space
        wrap
        align="center"
        style={{ marginBottom: 16, width: "100%", justifyContent: "space-between" }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <GlobalOutlined /> {domain.name}
        </Typography.Title>
        <RenameDomainButton
          domain={{ id: domain.id, name: domain.name }}
          onRenamed={() => void domainQ.refetch?.()}
        />
      </Space>

      {mobile ? (
        // Narrow screens: a full-width Select replaces the tab strip so tabs
        // don't scroll horizontally. Same URL-per-tab contract.
        <Card>
          <Select
            value={activeKey}
            onChange={(k) => navigate(`${LIST_PATH}/${domain.id}/${k}`)}
            options={tabs.map((tdef) => ({ value: tdef.key, label: tdef.label }))}
            style={{ width: "100%", marginBottom: 16 }}
            aria-label="Domain section"
          />
          {active.node}
        </Card>
      ) : (
        <Card
          tabList={tabs.map((tdef) => ({ key: tdef.key, tab: tdef.label }))}
          activeTabKey={activeKey}
          onTabChange={(k) => navigate(`${LIST_PATH}/${domain.id}/${k}`)}
        >
          {active.node}
        </Card>
      )}
    </div>
  );
};

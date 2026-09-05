// WebDomainPage — GH #1543 (johnnyq): clicking a domain in the tenant Web
// Domains list opens this dedicated page. Its per-domain actions are organised
// as navigable tabs (like the admin Edit Domain page) rather than a row of
// modal launchers. The tab lives in the URL (:tab), so a tab is linkable and
// the browser Back button walks the tabs.
//
// Tabs here: Overview (facts + the preview-URL / bot-challenge toggles), Index
// Files, Caching and Directory Privacy, plus three tabs gated on the same caps
// as the row menu — Domain options and Rewrite rules (tenant_domain_options_
// enabled) and Document root (tenant_docroot_editable). Redirects and folding
// DNS in move here in a follow-up, which also strips the row menu down to
// Disable/Delete.
import type { ReactNode } from "react";
import { Alert, Button, Card, Skeleton, Space, Typography } from "antd";
import { GlobalOutlined } from "@icons";
import { useNavigate, useParams } from "react-router";

import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { useOneQuery } from "../../../hooks/useQueries";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";
import type { Domain } from "../../../components/domains/types";
import { DomainCacheSection } from "../../../components/DomainCacheSection";
import { DomainDirectoryPrivacySection } from "../../admin/domains/DomainDirectoryPrivacySection";
import { DomainIndexPanel } from "../../DomainIndexPanel";
import { DomainNginxOptionsPanel } from "../../../components/DomainNginxOptionsPanel";
import { TenantNginxRulesPanel } from "../../DomainSettingsButton";
import { DomainDocRootPanel } from "../../../components/domains/DomainDocRootPanel";
import { OverviewTab } from "./tabs/OverviewTab";

const DEFAULT_TAB = "overview";
const LIST_PATH = "/jabali-panel/domains";

export const WebDomainPage = () => {
  const { id, tab } = useParams<{ id: string; tab?: string }>();
  const navigate = useNavigate();

  const domainQ = useOneQuery<Domain>({ resource: "domains", id });
  const { data: caps } = useServerCapabilities();

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

  const tabs: { key: string; label: string; node: ReactNode }[] = [
    { key: "overview", label: "Overview", node: <OverviewTab domain={domain} /> },
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
      </Space>

      <Card
        tabList={tabs.map((tdef) => ({ key: tdef.key, tab: tdef.label }))}
        activeTabKey={activeKey}
        onTabChange={(k) => navigate(`${LIST_PATH}/${domain.id}/${k}`)}
      >
        {active.node}
      </Card>
    </div>
  );
};

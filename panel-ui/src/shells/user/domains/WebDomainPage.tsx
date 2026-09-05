// WebDomainPage — GH #1543 (johnnyq): clicking a domain in the tenant Web
// Domains list opens this dedicated page. Its per-domain actions are organised
// as navigable tabs (like the admin Edit Domain page) rather than a row of
// modal launchers. The tab lives in the URL (:tab), so a tab is linkable and
// the browser Back button walks the tabs.
//
// Slice A (this PR) lands the shell + row-click navigation + breadcrumbs and
// three panes that need no extraction: Overview (facts + the preview-URL /
// bot-challenge toggles), Caching and Directory Privacy (both already shared
// Section components). The remaining actions (Redirects, Index, Domain options,
// Rewrite rules, Document root) and folding DNS in move here in a follow-up,
// which also strips the row menu down to Disable/Delete.
import { Alert, Button, Card, Skeleton, Space, Typography } from "antd";
import { GlobalOutlined } from "@icons";
import { useNavigate, useParams } from "react-router";

import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { useOneQuery } from "../../../hooks/useQueries";
import type { Domain } from "../../../components/domains/types";
import { DomainCacheSection } from "../../../components/DomainCacheSection";
import { DomainDirectoryPrivacySection } from "../../admin/domains/DomainDirectoryPrivacySection";
import { OverviewTab } from "./tabs/OverviewTab";

const TAB_KEYS = ["overview", "caching", "directory-privacy"] as const;
type TabKey = (typeof TAB_KEYS)[number];
const DEFAULT_TAB: TabKey = "overview";

const TAB_LABELS: Record<TabKey, string> = {
  overview: "Overview",
  caching: "Caching",
  "directory-privacy": "Directory Privacy",
};

const LIST_PATH = "/jabali-panel/domains";

export const WebDomainPage = () => {
  const { id, tab } = useParams<{ id: string; tab?: string }>();
  const navigate = useNavigate();

  const domainQ = useOneQuery<Domain>({ resource: "domains", id });
  const activeKey: TabKey = (TAB_KEYS as readonly string[]).includes(tab ?? "")
    ? (tab as TabKey)
    : DEFAULT_TAB;

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

  const renderTab = () => {
    switch (activeKey) {
      case "overview":
        return <OverviewTab domain={domain} />;
      case "caching":
        return <DomainCacheSection domainId={domain.id} />;
      case "directory-privacy":
        return <DomainDirectoryPrivacySection domainId={domain.id} domainName={domain.name} />;
    }
  };

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
        tabList={TAB_KEYS.map((k) => ({ key: k, tab: TAB_LABELS[k] }))}
        activeTabKey={activeKey}
        onTabChange={(k) => navigate(`${LIST_PATH}/${domain.id}/${k}`)}
      >
        {renderTab()}
      </Card>
    </div>
  );
};

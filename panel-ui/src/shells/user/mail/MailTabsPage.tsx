// MailTabsPage — unified mail management with tabs for mailboxes, forwarders, etc.
// Uses Card.tabList so the active-tab strip attaches to the card body (matches
// admin UserList styling).
import { Button, Card, Space, Typography } from "antd";
import { MailOutlined, PlusOutlined } from "@icons";
import { useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router";

import { useListQuery } from "../../../hooks/useQueries";
import type { Domain } from "../domains/UserDomainList";
import { MailboxesTab } from "./tabs/MailboxesTab";
import { GroupsTab } from "./tabs/GroupsTab";
import { ForwardersTab } from "./tabs/ForwardersTab";
import { CatchAllTab } from "./tabs/CatchAllTab";
import { DisclaimerTab } from "./tabs/DisclaimerTab";
import { SharedFoldersTab } from "./tabs/SharedFoldersTab";
import { SharedResourcesTab } from "./tabs/SharedResourcesTab";
import { LogsTab } from "./tabs/LogsTab";
import { StatisticsTab } from "./tabs/StatisticsTab";
import { CreateMailboxWizardModal } from "./CreateMailboxWizardModal";

const TAB_KEYS = ["mailboxes", "groups", "forwarders", "catchall", "disclaimer", "shared", "resources", "logs", "statistics"] as const;
type TabKey = (typeof TAB_KEYS)[number];
const DEFAULT_TAB: TabKey = "mailboxes";

const TAB_LABELS: Record<TabKey, string> = {
  mailboxes: "Mailboxes",
  groups: "Groups",
  forwarders: "Forwarders",
  catchall: "Catch-All",
  disclaimer: "Disclaimer",
  shared: "Shared Folders",
  resources: "Shared Resources",
  logs: "Logs",
  statistics: "Statistics",
};

export const MailTabsPage = () => {
  const [showCreateMailbox, setShowCreateMailbox] = useState(false);
  const { tab } = useParams<{ tab?: string }>();
  const navigate = useNavigate();
  const activeKey: TabKey = (TAB_KEYS as readonly string[]).includes(tab ?? "") ? (tab as TabKey) : DEFAULT_TAB;

  const { items: allDomains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });
  const mailDomains = useMemo(
    () => allDomains.filter((d) => d.email_enabled).map((d) => ({ id: d.id, name: d.name })),
    [allDomains],
  );

  const renderTab = () => {
    switch (activeKey) {
      case "mailboxes":
        return <MailboxesTab />;
      case "groups":
        return <GroupsTab />;
      case "forwarders":
        return <ForwardersTab />;
      case "catchall":
        return <CatchAllTab />;
      case "disclaimer":
        return <DisclaimerTab />;
      case "shared":
        return <SharedFoldersTab />;
      case "resources":
        return <SharedResourcesTab />;
      case "logs":
        return <LogsTab />;
      case "statistics":
        return <StatisticsTab />;
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
          <MailOutlined /> Mail
        </Typography.Title>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setShowCreateMailbox(true)}
        >
          New Mailbox
        </Button>
      </Space>

      <Card
        tabList={TAB_KEYS.map((k) => ({ key: k, tab: TAB_LABELS[k] }))}
        activeTabKey={activeKey}
        onTabChange={(k) => navigate(`/jabali-panel/mail/${k}`)}
      >
        {renderTab()}
      </Card>

      {showCreateMailbox && (
        <CreateMailboxWizardModal
          open={showCreateMailbox}
          domains={mailDomains}
          onCancel={() => setShowCreateMailbox(false)}
          onCreated={() => setShowCreateMailbox(false)}
        />
      )}
    </div>
  );
};

// MailDomainPage — GH #1387 drill-down: Mail → Mail Domains → this domain →
// accounts. Everything here is scoped to one domain (the :domainId route param,
// resolved through the owner-scoped GET /domains/:id so a foreign id renders an
// error, never another tenant's data). The account/domain tabs reuse the mail
// tab components in their single-domain mode. Logs + Statistics scoping and the
// retirement of the flat Mail page are a follow-up (PR-B).
import { Alert, Button, Card, Skeleton, Space, Typography } from "antd";
import { ArrowLeftOutlined, MailOutlined, PlusOutlined } from "@icons";
import { useState } from "react";
import { useNavigate, useParams } from "react-router";

import { useOneQuery } from "../../../hooks/useQueries";
import type { Domain } from "../domains/UserDomainList";
import { MailboxesTab } from "./tabs/MailboxesTab";
import { GroupsTab } from "./tabs/GroupsTab";
import { ForwardersTab } from "./tabs/ForwardersTab";
import { CatchAllTab } from "./tabs/CatchAllTab";
import { DisclaimerTab } from "./tabs/DisclaimerTab";
import { SharedFoldersTab } from "./tabs/SharedFoldersTab";
import { SharedResourcesTab } from "./tabs/SharedResourcesTab";
import { CreateMailboxWizardModal } from "./CreateMailboxWizardModal";

const TAB_KEYS = [
  "mailboxes",
  "forwarders",
  "groups",
  "shared",
  "resources",
  "catchall",
  "disclaimer",
] as const;
type TabKey = (typeof TAB_KEYS)[number];
const DEFAULT_TAB: TabKey = "mailboxes";

const TAB_LABELS: Record<TabKey, string> = {
  mailboxes: "Accounts",
  forwarders: "Forwarders",
  groups: "Groups",
  shared: "Shared Folders",
  resources: "Shared Resources",
  catchall: "Catch-All",
  disclaimer: "Disclaimer",
};

export const MailDomainPage = () => {
  const { domainId, tab } = useParams<{ domainId: string; tab?: string }>();
  const navigate = useNavigate();
  const [showCreateMailbox, setShowCreateMailbox] = useState(false);

  const domainQ = useOneQuery<Domain>({ resource: "domains", id: domainId });
  const activeKey: TabKey = (TAB_KEYS as readonly string[]).includes(tab ?? "")
    ? (tab as TabKey)
    : DEFAULT_TAB;

  const back = () => navigate("/jabali-panel/mail-domains");

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
          action={<Button onClick={back}>Back to Mail Domains</Button>}
        />
      </div>
    );
  }
  const domain = domainQ.data;

  const renderTab = () => {
    switch (activeKey) {
      case "mailboxes":
        return <MailboxesTab domainId={domainId} />;
      case "forwarders":
        return <ForwardersTab domainId={domainId} />;
      case "groups":
        return <GroupsTab domainId={domainId} />;
      case "shared":
        return <SharedFoldersTab domainId={domainId} />;
      case "resources":
        return <SharedResourcesTab domainId={domainId} />;
      case "catchall":
        return <CatchAllTab domainId={domainId} />;
      case "disclaimer":
        return <DisclaimerTab domainId={domainId} />;
    }
  };

  return (
    <div style={{ padding: "20px" }}>
      <Space wrap align="center" style={{ marginBottom: 8 }}>
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={back}>
          Mail Domains
        </Button>
      </Space>
      <Space
        wrap
        align="center"
        style={{ marginBottom: 16, width: "100%", justifyContent: "space-between" }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <MailOutlined /> {domain.name}
        </Typography.Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setShowCreateMailbox(true)}>
          New Mailbox
        </Button>
      </Space>

      <Card
        tabList={TAB_KEYS.map((k) => ({ key: k, tab: TAB_LABELS[k] }))}
        activeTabKey={activeKey}
        onTabChange={(k) => navigate(`/jabali-panel/mail-domains/${domainId}/${k}`)}
      >
        {renderTab()}
      </Card>

      {showCreateMailbox && (
        <CreateMailboxWizardModal
          open={showCreateMailbox}
          domains={[{ id: domain.id, name: domain.name }]}
          onCancel={() => setShowCreateMailbox(false)}
          onCreated={() => setShowCreateMailbox(false)}
        />
      )}
    </div>
  );
};

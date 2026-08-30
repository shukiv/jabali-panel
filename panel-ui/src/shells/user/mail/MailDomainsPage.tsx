// MailDomainsPage — GH #1387 foundation slice. A per-domain mail summary: the
// tenant's mail-enabled domains at a glance (mailbox count, storage, 30-day
// sent/received). The drill-down into a domain's accounts and the mailbox-tab
// migration are the operator's mail restructure; this is only the entry list.
import { Button, Card, Empty, Spin, Table, Typography } from "antd";
import { PlusOutlined } from "@icons";
import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { useListQuery } from "../../../hooks/useQueries";
import { humanBytes } from "../../../utils/bytes";
import { CreateMailboxWizardModal } from "./CreateMailboxWizardModal";

interface MailDomainRow {
  id: string;
  name: string;
  mailbox_count: number;
  mail_bytes: number;
  sent_30d: number;
  received_30d: number;
  // Omitted by the API when the queue couldn't be read (agent unavailable) —
  // shown as "—", never a misleading 0.
  queue?: number | null;
}

const num = (n: number | null | undefined): string => (n ?? 0).toLocaleString();

export function MailDomainsPage() {
  const navigate = useNavigate();
  const [showCreate, setShowCreate] = useState(false);
  const query = useListQuery<MailDomainRow>({ resource: "me/mail-domains" });
  const rows = query.items;
  const createDomains = useMemo(
    () => rows.map((r) => ({ id: r.id, name: r.name })),
    [rows],
  );

  return (
    <Card
      title="Mail Domains"
      extra={
        <Button
          type="primary"
          icon={<PlusOutlined />}
          disabled={createDomains.length === 0}
          onClick={() => setShowCreate(true)}
        >
          New Mailbox
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
        Your mail-enabled domains at a glance. Select a domain to manage its accounts.
      </Typography.Paragraph>
      {query.isLoading ? (
        <Spin />
      ) : rows.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="No mail-enabled domains."
        />
      ) : (
        <Table<MailDomainRow>
          rowKey="id"
          dataSource={rows}
          pagination={false}
          scroll={{ x: "max-content" }}
        >
          <Table.Column<MailDomainRow>
            title="Domain"
            dataIndex="name"
            key="name"
            render={(name: string, row) => (
              <a onClick={() => navigate(`/jabali-panel/mail-domains/${row.id}`)}>{name}</a>
            )}
          />
          <Table.Column<MailDomainRow>
            title="Mailboxes"
            dataIndex="mailbox_count"
            key="mailbox_count"
            align="right"
            render={(n: number) => num(n)}
          />
          <Table.Column<MailDomainRow>
            title="Mail storage"
            dataIndex="mail_bytes"
            key="mail_bytes"
            align="right"
            render={(n: number) => humanBytes(n)}
          />
          <Table.Column<MailDomainRow>
            title="Sent (30d)"
            dataIndex="sent_30d"
            key="sent_30d"
            align="right"
            render={(n: number) => num(n)}
          />
          <Table.Column<MailDomainRow>
            title="Received (30d)"
            dataIndex="received_30d"
            key="received_30d"
            align="right"
            render={(n: number) => num(n)}
          />
          <Table.Column<MailDomainRow>
            title="Queue"
            dataIndex="queue"
            key="queue"
            align="right"
            render={(q: number | null | undefined) =>
              q === null || q === undefined ? "—" : num(q)
            }
          />
        </Table>
      )}
      {showCreate && (
        <CreateMailboxWizardModal
          open={showCreate}
          domains={createDomains}
          onCancel={() => setShowCreate(false)}
          onCreated={() => setShowCreate(false)}
        />
      )}
    </Card>
  );
}

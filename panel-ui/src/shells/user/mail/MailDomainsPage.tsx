// MailDomainsPage — GH #1387. The tenant's Mail entry point: every domain they
// own, with at-a-glance mail info (mailbox count, storage, 30-day sent/received,
// queue), an SSL badge and a mail Status, plus a per-row Enable/Disable action.
//
// GH #1387 follow-up (johnnyq): columns sort, SSL + Status columns, and the
// Enable/Disable action. To make "Enable" meaningful the list now shows ALL
// owned domains (not just mail-enabled ones); a mail-off row carries
// email_enabled=false and can be turned on in place. Enable/Disable reuse the
// existing owner-scoped POST/DELETE /domains/:id/email endpoints.
import {
  Button,
  Card,
  Empty,
  Popconfirm,
  Spin,
  Table,
  Tag,
  Typography,
  type TableColumnsType,
} from "antd";
import { PlusOutlined } from "@icons";
import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { useListQuery } from "../../../hooks/useQueries";
import { humanBytes } from "../../../utils/bytes";
import { getSSLTagColor, getSSLTagLabel } from "../../../utils/sslState";
import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
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
  email_enabled: boolean;
  ssl_state?: string;
  is_quota_suspended?: boolean;
}

const num = (n: number | null | undefined): string => (n ?? 0).toLocaleString();
// nulls (unknown queue) sort last regardless of order.
const numOrNull = (n: number | null | undefined): number =>
  n === null || n === undefined ? -1 : n;

export function MailDomainsPage() {
  const navigate = useNavigate();
  const [showCreate, setShowCreate] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const query = useListQuery<MailDomainRow>({ resource: "me/mail-domains" });
  const rows = query.items;
  // Only mail-ENABLED domains can host a new mailbox, so the create wizard is
  // scoped to those even though the table also lists mail-off domains.
  const createDomains = useMemo(
    () => rows.filter((r) => r.email_enabled).map((r) => ({ id: r.id, name: r.name })),
    [rows],
  );

  const toggleMail = async (row: MailDomainRow) => {
    const enable = !row.email_enabled;
    setBusyId(row.id);
    try {
      if (enable) {
        const resp = await apiClient.post<{ warnings?: string[] }>(
          `/domains/${row.id}/email`,
        );
        feedback.message.success(`Mail enabled for ${row.name}`);
        const warnings = resp.data?.warnings ?? [];
        if (warnings.length > 0) {
          feedback.message.warning(warnings[0]);
        }
      } else {
        await apiClient.delete(`/domains/${row.id}/email`);
        feedback.message.success(`Mail disabled for ${row.name}`);
      }
      await query.refetch();
    } catch (err) {
      feedback.message.error(
        err instanceof Error ? err.message : "Could not change mail status",
      );
    } finally {
      setBusyId(null);
    }
  };

  const columns: TableColumnsType<MailDomainRow> = [
    {
      title: "Domain",
      dataIndex: "name",
      key: "name",
      sorter: (a, b) => a.name.localeCompare(b.name),
      defaultSortOrder: "ascend",
      render: (name: string, row) => (
        <a onClick={() => navigate(`/jabali-panel/mail-domains/${row.id}`)}>{name}</a>
      ),
    },
    {
      title: "Status",
      key: "status",
      sorter: (a, b) => Number(a.email_enabled) - Number(b.email_enabled),
      render: (_v, row) =>
        row.is_quota_suspended ? (
          <Tag color="red">Suspended</Tag>
        ) : row.email_enabled ? (
          <Tag color="green">Enabled</Tag>
        ) : (
          <Tag>Disabled</Tag>
        ),
    },
    {
      title: "SSL",
      dataIndex: "ssl_state",
      key: "ssl_state",
      sorter: (a, b) =>
        getSSLTagLabel(a.ssl_state).localeCompare(getSSLTagLabel(b.ssl_state)),
      render: (state?: string) => (
        <Tag color={getSSLTagColor(state)}>{getSSLTagLabel(state)}</Tag>
      ),
    },
    {
      title: "Mailboxes",
      dataIndex: "mailbox_count",
      key: "mailbox_count",
      align: "right",
      sorter: (a, b) => a.mailbox_count - b.mailbox_count,
      render: (n: number) => num(n),
    },
    {
      title: "Mail storage",
      dataIndex: "mail_bytes",
      key: "mail_bytes",
      align: "right",
      sorter: (a, b) => a.mail_bytes - b.mail_bytes,
      render: (n: number) => humanBytes(n),
    },
    {
      title: "Sent (30d)",
      dataIndex: "sent_30d",
      key: "sent_30d",
      align: "right",
      sorter: (a, b) => a.sent_30d - b.sent_30d,
      render: (n: number) => num(n),
    },
    {
      title: "Received (30d)",
      dataIndex: "received_30d",
      key: "received_30d",
      align: "right",
      sorter: (a, b) => a.received_30d - b.received_30d,
      render: (n: number) => num(n),
    },
    {
      title: "Queue",
      dataIndex: "queue",
      key: "queue",
      align: "right",
      sorter: (a, b) => numOrNull(a.queue) - numOrNull(b.queue),
      render: (q: number | null | undefined) =>
        q === null || q === undefined ? "—" : num(q),
    },
    {
      title: "Actions",
      key: "actions",
      render: (_v, row) =>
        row.email_enabled ? (
          <Popconfirm
            title={`Disable mail for ${row.name}?`}
            description="Incoming mail stops and the managed mail DNS records are removed. Mailboxes are kept — re-enabling restores service."
            okText="Disable"
            okButtonProps={{ danger: true }}
            onConfirm={() => toggleMail(row)}
          >
            <Button size="small" danger loading={busyId === row.id}>
              Disable
            </Button>
          </Popconfirm>
        ) : (
          <Popconfirm
            title={`Enable mail for ${row.name}?`}
            description="Sets up DKIM and publishes the recommended mail DNS records for this domain."
            okText="Enable"
            onConfirm={() => toggleMail(row)}
          >
            <Button size="small" loading={busyId === row.id}>
              Enable
            </Button>
          </Popconfirm>
        ),
    },
  ];

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
        Your domains and their mail at a glance. Select a domain to manage its
        accounts, or enable mail on a domain that doesn&apos;t have it yet.
      </Typography.Paragraph>
      {query.isLoading ? (
        <Spin />
      ) : rows.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No domains yet." />
      ) : (
        <Table<MailDomainRow>
          rowKey="id"
          dataSource={rows}
          columns={columns}
          pagination={false}
          scroll={{ x: "max-content" }}
        />
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

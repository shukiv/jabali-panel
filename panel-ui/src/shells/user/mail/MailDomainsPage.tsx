// MailDomainsPage — GH #1387. The tenant's Mail entry point: every domain they
// own that HAS mail active, with at-a-glance info (mailbox count, storage,
// 30-day sent/received, queue), an SSL badge, and a per-row Disable action.
//
// GH #1387 (johnnyq, 2026-09-01): the list shows ONLY mail-active domains (the
// earlier "show every owned domain + a Status column + Enable action" is
// reverted). Enabling mail on a domain is a Domains-page action; creating a
// mailbox happens inside a domain's drill-down, not here. Disable reuses the
// existing owner-scoped DELETE /domains/:id/email endpoint. Delete is the
// destructive mail-only teardown (POST /domains/:id/email/purge) — it removes
// mailboxes + mail certs + mail DNS but keeps the website + DNS zone.
import {
  Alert,
  Button,
  Card,
  Dropdown,
  Empty,
  Input,
  Modal,
  Spin,
  Table,
  Tag,
  Typography,
  type TableColumnsType,
} from "antd";
import { DeleteOutlined, MoreOutlined, PoweroffOutlined } from "@icons";
import { useState } from "react";
import { useNavigate } from "react-router";
import { useListQuery } from "../../../hooks/useQueries";
import { humanBytes } from "../../../utils/bytes";
import { getSSLTagColor, getSSLTagLabel } from "../../../utils/sslState";
import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

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
  const [busyId, setBusyId] = useState<string | null>(null);
  // Delete (mail purge) is type-to-confirm: the row being deleted + the typed
  // domain name that must match before the destructive button enables.
  const [deleteRow, setDeleteRow] = useState<MailDomainRow | null>(null);
  const [confirmText, setConfirmText] = useState("");
  const query = useListQuery<MailDomainRow>({ resource: "me/mail-domains" });
  const rows = query.items;

  // Every listed domain is mail-active, so the only mail-state action here is
  // Disable (soft — keeps mailboxes, re-enable from the Domains page restores
  // service). It reuses the owner-scoped DELETE /domains/:id/email endpoint.
  const disableMail = async (row: MailDomainRow) => {
    setBusyId(row.id);
    try {
      await apiClient.delete(`/domains/${row.id}/email`);
      feedback.message.success(`Mail disabled for ${row.name}`);
      await query.refetch();
    } catch (err) {
      feedback.message.error(
        err instanceof Error ? err.message : "Could not disable mail",
      );
    } finally {
      setBusyId(null);
    }
  };

  const confirmDisable = (row: MailDomainRow) => {
    feedback.modal.confirm({
      title: `Disable mail for ${row.name}?`,
      content:
        "Incoming mail stops and the managed mail DNS records are removed. Mailboxes are kept — re-enabling from the Domains page restores service.",
      okText: "Disable",
      okButtonProps: { danger: true },
      onOk: () => disableMail(row),
    });
  };

  // Delete = the destructive mail-only teardown (POST /domains/:id/email/purge).
  // The server also requires confirm_domain to equal the name, so a stray call
  // can't wipe a domain's mail.
  const purgeMail = async (row: MailDomainRow) => {
    setBusyId(row.id);
    try {
      const resp = await apiClient.post<{ warnings?: string[] }>(
        `/domains/${row.id}/email/purge`,
        { confirm_domain: row.name },
      );
      feedback.message.success(`Mail deleted for ${row.name}`);
      (resp.data?.warnings ?? []).forEach((w) => feedback.message.warning(w));
      setDeleteRow(null);
      setConfirmText("");
      await query.refetch();
    } catch (err) {
      feedback.message.error(
        err instanceof Error ? err.message : "Could not delete mail",
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
      // GH #1387 (johnnyq): collapse Disable + Delete into one "⋯" menu so the
      // row stays compact. Disable confirms via a modal (a Popconfirm can't
      // anchor cleanly inside a dropdown); Delete opens the type-to-confirm modal.
      render: (_v, row) => (
        <Dropdown
          trigger={["click"]}
          menu={{
            items: [
              {
                key: "disable",
                icon: <PoweroffOutlined />,
                danger: true,
                label: "Disable",
                onClick: () => confirmDisable(row),
              },
              {
                key: "delete",
                icon: <DeleteOutlined />,
                danger: true,
                label: "Delete",
                onClick: () => {
                  setConfirmText("");
                  setDeleteRow(row);
                },
              },
            ],
          }}
        >
          <Button
            size="small"
            icon={<MoreOutlined />}
            loading={busyId === row.id}
            aria-label={`Actions for ${row.name}`}
          />
        </Dropdown>
      ),
    },
  ];

  return (
    <Card title="Mail Domains">
      <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
        Domains with mail active, at a glance. Select a domain to manage its
        accounts and add mailboxes.
      </Typography.Paragraph>
      {query.isLoading ? (
        <Spin />
      ) : rows.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="No domains have mail active yet. Enable mail on a domain from the Domains page."
        />
      ) : (
        <Table<MailDomainRow>
          rowKey="id"
          dataSource={rows}
          columns={columns}
          pagination={false}
          scroll={{ x: "max-content" }}
        />
      )}

      <Modal
        open={!!deleteRow}
        title={deleteRow ? `Delete mail for ${deleteRow.name}?` : "Delete mail"}
        okText="Delete mail"
        okButtonProps={{
          danger: true,
          loading: busyId === deleteRow?.id,
          disabled: !deleteRow || confirmText.trim() !== deleteRow.name,
        }}
        onOk={() => deleteRow && purgeMail(deleteRow)}
        onCancel={() => setDeleteRow(null)}
        destroyOnClose
      >
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message="This permanently deletes the mail service for this domain"
          description={
            <ul style={{ margin: "8px 0 0", paddingLeft: 18 }}>
              <li>All mailboxes in this domain and the mail they store</li>
              <li>The mail TLS certificate (mail, autodiscover, autoconfig)</li>
              <li>The managed mail DNS records (MX, SPF, DKIM, autodiscover…)</li>
            </ul>
          }
        />
        <Typography.Paragraph style={{ marginBottom: 8 }}>
          The website, its DNS zone, and its certificate are kept. Type{" "}
          <Typography.Text code>{deleteRow?.name}</Typography.Text> to confirm.
        </Typography.Paragraph>
        <Input
          autoFocus
          placeholder={deleteRow?.name}
          value={confirmText}
          onChange={(e) => setConfirmText(e.target.value)}
          onPressEnter={() => {
            if (deleteRow && confirmText.trim() === deleteRow.name) purgeMail(deleteRow);
          }}
        />
      </Modal>
    </Card>
  );
}

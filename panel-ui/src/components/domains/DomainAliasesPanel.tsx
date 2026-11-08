// GH #1625 — web domain aliases: more names for the same site.
//
// The Aliases tab on the tenant Web Domain page and on the admin Edit Domain
// page. An alias is served from the domain's own vhost and document root; the
// panel adds it to the certificate once it resolves directly to this server
// (a CDN-fronted alias is left off on purpose).
//
// An alias has two separate waits, and the row shows the first that applies:
//   - ownership (GH #1816): a name outside the owner's verified domains needs
//     its own TXT record before it goes live, so the row shows that record and
//     Verify now;
//   - certificate: a live alias not on the certificate yet shows what to point
//     at this server (pending_reason).
// Telling a tenant to fix an A record while the TXT record is what's missing
// would send them the wrong way.
import { useState } from "react";
import { Button, Form, Input, Popconfirm, Space, Table, Tag, Tooltip, Typography } from "antd";
import { CheckOutlined, CloseOutlined, PlusOutlined, ReloadOutlined } from "@icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { extractApiError } from "../../apiErrors";
import { feedback } from "../../lib/feedback";
import { shortDateTime } from "../../utils/datetime";
import { CollapsibleAlert } from "../CollapsibleAlert";
import { RowActionButton } from "../RowActionButton";
import { RowDeleteButton } from "../RowDeleteButton";
import type { Domain } from "./types";
import { OWNERSHIP_METHOD_TEXT, OWNERSHIP_RESULT_TEXT, type OwnershipView } from "./ownership";

export type DomainAlias = {
  id: string;
  domain_id: string;
  hostname: string;
  cert_status: "active" | "pending" | string;
  pending_reason?: string;
  ownership: OwnershipView;
  created_at: string;
};

type AliasList = { data: DomainAlias[]; total: number };

export const aliasesQueryKey = (domainID: string) => ["domain-aliases", domainID];

const aliasVerified = (a: DomainAlias) => a.ownership?.status === "verified";

const StatusTag = ({ alias }: { alias: DomainAlias }) => {
  if (!aliasVerified(alias)) {
    return (
      <Tooltip title="Not live yet: it waits for a TXT record that proves you own the name.">
        <Tag color="orange">Unverified</Tag>
      </Tooltip>
    );
  }
  if (alias.cert_status !== "active") {
    return (
      <Tooltip title="Served, but not on the certificate yet, so HTTPS shows a warning for this name.">
        <Tag color="gold">Certificate pending</Tag>
      </Tooltip>
    );
  }
  return <Tag color="green">Active</Tag>;
};

const Details = ({ alias }: { alias: DomainAlias }) => {
  const o = alias.ownership;
  if (!aliasVerified(alias)) {
    return (
      <Space direction="vertical" size={2} style={{ maxWidth: 420 }}>
        <Typography.Text type="secondary">Add this TXT record at the name&apos;s DNS provider:</Typography.Text>
        <Typography.Text>
          Name:{" "}
          <Typography.Text code copyable>
            {o.challenge_name}
          </Typography.Text>
        </Typography.Text>
        <Typography.Text>Value:</Typography.Text>
        <Typography.Text code copyable style={{ wordBreak: "break-all" }}>
          {o.challenge_value}
        </Typography.Text>
        {o.last_result ? (
          <Typography.Text type="secondary">
            Last check{o.checked_at ? ` (${shortDateTime(o.checked_at)})` : ""}:{" "}
            {OWNERSHIP_RESULT_TEXT[o.last_result] ?? o.last_result}
          </Typography.Text>
        ) : null}
        {o.expires_at ? (
          <Typography.Text type="secondary">
            Removed on {shortDateTime(o.expires_at)} if it is not verified by then.
          </Typography.Text>
        ) : null}
      </Space>
    );
  }
  if (alias.cert_status !== "active") {
    return (
      <Typography.Text type="secondary" style={{ display: "block", maxWidth: 420 }}>
        {alias.pending_reason}
      </Typography.Text>
    );
  }
  return <Typography.Text type="secondary">On the certificate.</Typography.Text>;
};

type Props = {
  domain: Pick<Domain, "id" | "name">;
  // admin adds Approve (without a DNS proof) and Revoke.
  admin?: boolean;
};

export const DomainAliasesPanel = ({ domain, admin = false }: Props) => {
  const qc = useQueryClient();
  const [form] = Form.useForm<{ hostname: string }>();
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);

  const listQ = useQuery({
    queryKey: aliasesQueryKey(domain.id),
    queryFn: async () => (await apiClient.get<AliasList>(`/domains/${domain.id}/aliases`)).data,
  });

  // An alias changes the domain's names, its certificate and, for an admin,
  // the list of names waiting for proof.
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: aliasesQueryKey(domain.id) });
    void qc.invalidateQueries({ queryKey: ["list", "domains"] });
    void qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
    void qc.invalidateQueries({ queryKey: ["domain-ssl", domain.id] });
    void qc.invalidateQueries({ queryKey: ["admin", "domain-ownership", "pending"] });
  };

  const add = async ({ hostname }: { hostname: string }) => {
    setAdding(true);
    try {
      const { data } = await apiClient.post<DomainAlias>(`/domains/${domain.id}/aliases`, {
        hostname: hostname.trim(),
      });
      form.resetFields();
      if (aliasVerified(data)) {
        feedback.message.success(`${data.hostname} added. It is served within a minute.`);
      } else {
        feedback.message.info(`${data.hostname} added. Add its TXT record (below) to put it live.`);
      }
    } catch (err) {
      feedback.message.error(`Could not add the alias: ${extractApiError(err)}`);
    } finally {
      setAdding(false);
      refresh();
    }
  };

  const verify = async (a: DomainAlias) => {
    setBusy(`verify-${a.id}`);
    try {
      const { data } = await apiClient.post<{ result: string }>(
        `/domains/${domain.id}/aliases/${a.id}/ownership/verify`,
      );
      if (data.result === "verified") {
        feedback.message.success(`${a.hostname} is verified. It is served within a minute.`);
      } else {
        feedback.message.info(`${a.hostname}: ${OWNERSHIP_RESULT_TEXT[data.result] ?? data.result}`);
      }
    } catch (err) {
      feedback.message.error(`Check failed: ${extractApiError(err)}`);
    } finally {
      setBusy(null);
      refresh();
    }
  };

  const adminAction = async (a: DomainAlias, action: "approve" | "revoke") => {
    setBusy(`${action}-${a.id}`);
    try {
      await apiClient.post(`/admin/domain-ownership/aliases/${a.id}/${action}`);
      feedback.message.success(
        action === "approve"
          ? `${a.hostname} approved. It is served within a minute.`
          : `${a.hostname} is waiting for proof again and stops being served within a minute.`,
      );
    } catch (err) {
      feedback.message.error(`${action === "approve" ? "Approve" : "Revoke"} failed: ${extractApiError(err)}`);
    } finally {
      setBusy(null);
      refresh();
    }
  };

  const remove = async (a: DomainAlias) => {
    try {
      await apiClient.delete(`/domains/${domain.id}/aliases/${a.id}`);
    } finally {
      refresh();
    }
  };

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <CollapsibleAlert
        storageKey="domains.aliases.intro"
        type="info"
        showIcon
        message="Aliases serve this site under other names"
        description={
          <>
            Each alias opens the same site as {domain.name}, from the same files. For www.{domain.name}, use the
            domain&apos;s www option instead. An alias is added to the SSL certificate once its A or AAAA
            record points directly at this server; a name behind a CDN is not added. A name that isn&apos;t
            under one of your verified domains needs a TXT record to prove you own it before it goes live.
          </>
        }
      />

      <Form form={form} layout="inline" onFinish={add} style={{ rowGap: 8 }}>
        <Form.Item
          name="hostname"
          rules={[{ required: true, whitespace: true, message: "Enter a hostname" }]}
          style={{ flex: "1 1 260px", marginInlineEnd: 8 }}
        >
          <Input placeholder="example.net" aria-label="Alias hostname" autoComplete="off" maxLength={253} />
        </Form.Item>
        <Form.Item style={{ marginInlineEnd: 0 }}>
          <Button type="primary" htmlType="submit" icon={<PlusOutlined />} loading={adding}>
            Add alias
          </Button>
        </Form.Item>
      </Form>

      <Table<DomainAlias>
        rowKey="id"
        size="small"
        loading={listQ.isLoading}
        dataSource={listQ.data?.data ?? []}
        pagination={false}
        scroll={{ x: "max-content" }}
        locale={{
          emptyText: listQ.isError ? `Could not load the aliases: ${extractApiError(listQ.error)}` : "No aliases",
        }}
      >
        <Table.Column<DomainAlias> title="Alias" dataIndex="hostname" />
        <Table.Column<DomainAlias>
          title="Status"
          render={(_, a) => (
            <Space direction="vertical" size={0}>
              <StatusTag alias={a} />
              {admin && aliasVerified(a) && a.ownership.method ? (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  by {OWNERSHIP_METHOD_TEXT[a.ownership.method] ?? a.ownership.method}
                </Typography.Text>
              ) : null}
            </Space>
          )}
        />
        <Table.Column<DomainAlias> title="Details" render={(_, a) => <Details alias={a} />} />
        <Table.Column<DomainAlias>
          title="Actions"
          render={(_, a) => (
            <Space wrap>
              {!aliasVerified(a) ? (
                <RowActionButton
                  icon={<ReloadOutlined />}
                  loading={busy === `verify-${a.id}`}
                  onClick={() => void verify(a)}
                >
                  Verify now
                </RowActionButton>
              ) : null}
              {admin && !aliasVerified(a) ? (
                <RowActionButton
                  icon={<CheckOutlined />}
                  loading={busy === `approve-${a.id}`}
                  onClick={() =>
                    feedback.modal.confirm({
                      title: `Approve ${a.hostname} without a DNS proof?`,
                      content: "Approve only when you know who owns this name.",
                      okText: "Approve",
                      onOk: () => adminAction(a, "approve"),
                    })
                  }
                >
                  Approve
                </RowActionButton>
              ) : null}
              {admin && aliasVerified(a) ? (
                <Popconfirm
                  title={`Revoke the verification of ${a.hostname}?`}
                  description="It stops being served until its owner proves the name again."
                  okText="Revoke"
                  okButtonProps={{ danger: true }}
                  onConfirm={() => adminAction(a, "revoke")}
                >
                  <RowActionButton danger icon={<CloseOutlined />} loading={busy === `revoke-${a.id}`}>
                    Revoke
                  </RowActionButton>
                </Popconfirm>
              ) : null}
              <RowDeleteButton
                confirmTitle={`Remove the alias ${a.hostname}?`}
                successMessage={`${a.hostname} removed`}
                onConfirm={() => remove(a)}
                onError={(err) => feedback.message.error(`Could not remove the alias: ${extractApiError(err)}`)}
              />
            </Space>
          )}
        />
      </Table>
    </Space>
  );
};

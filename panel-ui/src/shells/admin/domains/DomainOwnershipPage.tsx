// DomainOwnershipPage — GH #1816 / ADR-0170. The admin's view of the domain
// ownership proof: the server-wide "require proof" switch, and the domains
// and web aliases waiting for their owner to prove the name, each with its
// last check result, its expiry and an Approve action. Approving is for a name
// whose owner the admin knows but cannot prove it by DNS (nameservers that
// already point here, a registrar that cannot add TXT records).
import { useState } from "react";
import { Alert, Card, Space, Switch, Table, Tag, Tooltip, Typography } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { CheckOutlined, ReloadOutlined, SafetyCertificateOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { shortDateTime } from "../../../utils/datetime";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { adminLinks } from "../../../components/admin/entityLinks";
import { RowActionButton } from "../../../components/RowActionButton";
import { OWNERSHIP_RESULT_TEXT, type OwnershipView } from "../../../components/domains/ownership";

type PendingDomain = { id: string; name: string; user_id: string; username?: string; ownership: OwnershipView };
type PendingAlias = {
  id: string;
  hostname: string;
  domain_id: string;
  domain_name: string;
  user_id: string;
  username?: string;
  ownership: OwnershipView;
};
type PendingResponse = { data: PendingDomain[]; total: number; aliases: PendingAlias[] };
type Settings = { require_proof: boolean; updated_by?: string; updated_at?: string };

const PENDING_QUERY_KEY = ["admin", "domain-ownership", "pending"];
const SETTINGS_QUERY_KEY = ["admin", "domain-ownership", "settings"];

const errText = (err: unknown): string => {
  const e = err as { response?: { data?: { detail?: string; error?: string } } };
  return e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message;
};

const resultCell = (v: OwnershipView) =>
  v.last_result ? (
    <Tooltip title={OWNERSHIP_RESULT_TEXT[v.last_result] ?? v.last_result}>
      <Tag color={v.last_result === "ns_points_here" || v.last_result === "dns_unresolvable" ? "red" : "default"}>
        {v.last_result}
      </Tag>
    </Tooltip>
  ) : (
    <Typography.Text type="secondary">not checked yet</Typography.Text>
  );

const expiresCell = (v: OwnershipView) =>
  v.expires_at ? (
    shortDateTime(v.expires_at)
  ) : (
    <Tooltip title="Verified once and sent back by an administrator, or the panel's own row: it is never removed.">
      <Typography.Text type="secondary">never</Typography.Text>
    </Tooltip>
  );

const ownerCell = (userID: string, username?: string) =>
  userID ? <Link to={adminLinks.user(userID)}>{username || userID.substring(0, 8)}</Link> : "—";

export const DomainOwnershipPage = () => {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);

  useSetBreadcrumbs([
    { title: "Web Domains", href: "/jabali-admin/domains" },
    { title: "Ownership proof" },
  ]);

  const pendingQ = useQuery({
    queryKey: PENDING_QUERY_KEY,
    queryFn: async () => (await apiClient.get<PendingResponse>("/admin/domain-ownership/pending")).data,
  });
  const settingsQ = useQuery({
    queryKey: SETTINGS_QUERY_KEY,
    queryFn: async () => (await apiClient.get<Settings>("/admin/domain-ownership/settings")).data,
  });

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: PENDING_QUERY_KEY });
    void qc.invalidateQueries({ queryKey: ["list", "domains"] });
  };

  const run = async (key: string, path: string, success: string) => {
    setBusy(key);
    try {
      await apiClient.post(path);
      feedback.message.success(success);
    } catch (err) {
      feedback.message.error(errText(err));
    } finally {
      setBusy(null);
      refresh();
    }
  };

  const verify = async (key: string, path: string, name: string) => {
    setBusy(key);
    try {
      const { data } = await apiClient.post<{ result: string }>(path);
      if (data.result === "verified") feedback.message.success(`${name} is verified.`);
      else feedback.message.info(`${name}: ${OWNERSHIP_RESULT_TEXT[data.result] ?? data.result}`);
    } catch (err) {
      feedback.message.error(errText(err));
    } finally {
      setBusy(null);
      refresh();
    }
  };

  const putSettings = async (require: boolean) => {
    setBusy("settings");
    try {
      const { data } = await apiClient.put<Settings & { warning?: string }>("/admin/domain-ownership/settings", {
        require_proof: require,
      });
      if (data.warning) feedback.message.warning(data.warning, 8);
      else feedback.message.success("New domain names need ownership proof again.");
    } catch (err) {
      feedback.message.error(errText(err));
    } finally {
      setBusy(null);
      void qc.invalidateQueries({ queryKey: SETTINGS_QUERY_KEY });
    }
  };

  const toggleRequire = (next: boolean) => {
    if (next) {
      void putSettings(true);
      return;
    }
    feedback.modal.confirm({
      title: "Stop requiring ownership proof?",
      content:
        "Tenants can then add any domain name without proving they control it, including names that belong to someone else, and those names go live at once. Names added while proof is off stay live when you switch it back on. Domains already waiting for proof stay pending.",
      okText: "Stop requiring proof",
      okButtonProps: { danger: true },
      onOk: () => putSettings(false),
    });
  };

  const settings = settingsQ.data;
  const pending = pendingQ.data;

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Title level={3} style={{ margin: 0 }}>
        <SafetyCertificateOutlined /> Domain ownership proof
      </Typography.Title>

      <Card>
        <Space align="start">
          <Switch
            checked={settings?.require_proof ?? true}
            loading={settingsQ.isLoading || busy === "settings"}
            onChange={toggleRequire}
            aria-label="Require ownership proof"
          />
          <div>
            <Typography.Text strong>Require ownership proof for new domain names</Typography.Text>
            <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
              A domain a tenant adds stays offline (no DNS zone, no mail, no trusted certificate) until its
              owner adds a TXT record at the domain&apos;s DNS provider. Names an administrator adds, and names
              under a domain the same owner already proved, are live at once. Unproven names are removed after
              14 days; their site files are kept.
            </Typography.Paragraph>
            {settings && !settings.require_proof ? (
              <Alert
                style={{ marginTop: 8 }}
                type="error"
                showIcon
                message="Proof is off: any tenant can claim any domain name on this server."
              />
            ) : null}
          </div>
        </Space>
      </Card>

      <Card
        title={`Domains waiting for proof${pending ? ` (${pending.total})` : ""}`}
        extra={
          <RowActionButton icon={<ReloadOutlined />} onClick={refresh} loading={pendingQ.isFetching}>
            Refresh
          </RowActionButton>
        }
      >
        {pendingQ.isError ? (
          <Alert type="error" showIcon message={`Could not load the list: ${errText(pendingQ.error)}`} />
        ) : (
          <Table<PendingDomain>
            rowKey="id"
            size="small"
            loading={pendingQ.isLoading}
            dataSource={pending?.data ?? []}
            pagination={false}
            scroll={{ x: "max-content" }}
            locale={{ emptyText: "No domain is waiting for proof." }}
          >
            <Table.Column<PendingDomain>
              title="Domain"
              dataIndex="name"
              render={(name: string, row) => <Link to={`/jabali-admin/domains/edit/${row.id}`}>{name}</Link>}
            />
            <Table.Column<PendingDomain> title="Owner" render={(_, row) => ownerCell(row.user_id, row.username)} />
            <Table.Column<PendingDomain>
              title="Pending since"
              render={(_, row) => shortDateTime(row.ownership.pending_since ?? null)}
            />
            <Table.Column<PendingDomain> title="Last check" render={(_, row) => resultCell(row.ownership)} />
            <Table.Column<PendingDomain> title="Removed on" render={(_, row) => expiresCell(row.ownership)} />
            <Table.Column<PendingDomain>
              title="Actions"
              render={(_, row) => (
                <Space>
                  <RowActionButton
                    icon={<ReloadOutlined />}
                    loading={busy === `verify-${row.id}`}
                    onClick={() => verify(`verify-${row.id}`, `/domains/${row.id}/ownership/verify`, row.name)}
                  >
                    Check now
                  </RowActionButton>
                  <RowActionButton
                    icon={<CheckOutlined />}
                    loading={busy === `approve-${row.id}`}
                    onClick={() =>
                      feedback.modal.confirm({
                        title: `Approve ${row.name} without a DNS proof?`,
                        content: "Approve only when you know who owns this name. It is published at once.",
                        okText: "Approve",
                        onOk: () =>
                          run(
                            `approve-${row.id}`,
                            `/admin/domain-ownership/domains/${row.id}/approve`,
                            `${row.name} approved. It is published within a minute.`,
                          ),
                      })
                    }
                  >
                    Approve
                  </RowActionButton>
                </Space>
              )}
            />
          </Table>
        )}
      </Card>

      {pending && pending.aliases.length > 0 ? (
        <Card title={`Web aliases waiting for proof (${pending.aliases.length})`}>
          <Table<PendingAlias>
            rowKey="id"
            size="small"
            dataSource={pending.aliases}
            pagination={false}
            scroll={{ x: "max-content" }}
          >
            <Table.Column<PendingAlias> title="Alias" dataIndex="hostname" />
            <Table.Column<PendingAlias>
              title="Domain"
              render={(_, row) => <Link to={`/jabali-admin/domains/edit/${row.domain_id}`}>{row.domain_name}</Link>}
            />
            <Table.Column<PendingAlias> title="Owner" render={(_, row) => ownerCell(row.user_id, row.username)} />
            <Table.Column<PendingAlias>
              title="Record"
              render={(_, row) => (
                <Typography.Text code copyable={{ text: row.ownership.challenge_value }}>
                  {row.ownership.challenge_name}
                </Typography.Text>
              )}
            />
            <Table.Column<PendingAlias> title="Last check" render={(_, row) => resultCell(row.ownership)} />
            <Table.Column<PendingAlias> title="Removed on" render={(_, row) => expiresCell(row.ownership)} />
            <Table.Column<PendingAlias>
              title="Actions"
              render={(_, row) => (
                <Space>
                  <RowActionButton
                    icon={<ReloadOutlined />}
                    loading={busy === `verify-${row.id}`}
                    onClick={() =>
                      verify(
                        `verify-${row.id}`,
                        `/domains/${row.domain_id}/aliases/${row.id}/ownership/verify`,
                        row.hostname,
                      )
                    }
                  >
                    Check now
                  </RowActionButton>
                  <RowActionButton
                    icon={<CheckOutlined />}
                    loading={busy === `approve-${row.id}`}
                    onClick={() =>
                      feedback.modal.confirm({
                        title: `Approve ${row.hostname} without a DNS proof?`,
                        content: "Approve only when you know who owns this name.",
                        okText: "Approve",
                        onOk: () =>
                          run(
                            `approve-${row.id}`,
                            `/admin/domain-ownership/aliases/${row.id}/approve`,
                            `${row.hostname} approved.`,
                          ),
                      })
                    }
                  >
                    Approve
                  </RowActionButton>
                </Space>
              )}
            />
          </Table>
        </Card>
      ) : null}
    </Space>
  );
};

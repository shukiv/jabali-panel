// User API Tokens — list / mint / revoke for the signed-in tenant.
//
// Wire path:   GET    /api/v1/me/api-tokens
//              POST   /api/v1/me/api-tokens     {name, expires_in_seconds?, scopes?}
//                       → {token: {…}, secret: "jat_…"}  (plaintext shown ONCE)
//              DELETE /api/v1/me/api-tokens/:id
//
// The "shown once" rule is enforced server-side (the plaintext is
// never persisted), so this page MUST surface it loudly in a modal
// the user can't dismiss without copying. Anything else risks a
// "wait, what was that secret?" support ticket.

import { useTranslation } from "react-i18next";
import { useEffect, useMemo, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Radio,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  notification,
} from "antd";
import type { ColumnsType } from "antd/es/table";

// GH #245 / ADR-0144: per-area permissions. Empty scopes = full access (the
// historical default). AREAS mirrors the panel-api scope vocabulary.
const AREAS: { key: string; label: string }[] = [
  { key: "dns", label: "DNS" },
  { key: "mail", label: "Mail" },
  { key: "files", label: "Files" },
  { key: "databases", label: "Databases" },
  { key: "apps", label: "Applications" },
  { key: "domains", label: "Domains" },
  { key: "ssl", label: "SSL" },
  { key: "php", label: "PHP" },
  { key: "cron", label: "Cron" },
  { key: "ssh", label: "SSH keys" },
  { key: "backups", label: "Backups" },
  { key: "logs", label: "Logs" },
  { key: "notifications", label: "Notifications" },
];

const SCOPE_LABELS: Record<string, string> = { ddns: "DDNS" };
for (const a of AREAS) {
  SCOPE_LABELS["read:" + a.key] = a.label + " read";
  SCOPE_LABELS["write:" + a.key] = a.label + " write";
}
import {
  CopyOutlined,
  DeleteOutlined,
  KeyOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import { apiClient } from "../../../apiClient";
import { APIDocsPage } from "../../shared/APIDocsPage";
import { DDNSSetupGuide } from "./DDNSSetupGuide";
import { MCPSetupGuide } from "./MCPSetupGuide";

type UserAPIToken = {
  id: string;
  user_id: string;
  name: string;
  secret_last4: string;
  scopes: string[] | null;
  last_used_at: string | null;
  last_used_ip: string | null;
  expires_at: string | null;
  revoked_at: string | null;
  created_at: string;
};

type CreateResponse = {
  token: UserAPIToken;
  secret: string;
};

// fmtDate prints ISO timestamps as "YYYY-MM-DD HH:mm" UTC for
// consistency across browsers (Intl.DateTimeFormat locale formats
// drift between Chrome / Safari / Firefox locales).
function fmtDate(s: string | null): string {
  if (!s) return "—";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return (
    d.toISOString().slice(0, 16).replace("T", " ") + " UTC"
  );
}

// statusTag projects (revoked, expires_at) into a single coloured tag.
function statusTag(t: UserAPIToken): JSX.Element {
  if (t.revoked_at) {
    return <Tag color="default">Revoked</Tag>;
  }
  if (t.expires_at && new Date(t.expires_at).getTime() < Date.now()) {
    return <Tag color="orange">Expired</Tag>;
  }
  return <Tag color="green">Active</Tag>;
}

export function UserAPITokensPage(): JSX.Element {
  const { t } = useTranslation();
  const [rows, setRows] = useState<UserAPIToken[]>([]);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [secret, setSecret] = useState<{ secret: string; name: string } | null>(
    null,
  );
  const [createForm] = Form.useForm<{
    name: string;
    expires_in_seconds?: number;
  }>();
  const [accessMode, setAccessMode] = useState<"full" | "custom">("full");
  const [scopeSel, setScopeSel] = useState<Record<string, string[]>>({});
  const [ddnsSel, setDdnsSel] = useState(false);
  const [ddnsRecordId, setDdnsRecordId] = useState("");

  const load = async () => {
    setLoading(true);
    try {
      const resp = await apiClient.get<{ items: UserAPIToken[] }>(
        "/me/api-tokens",
      );
      setRows(resp.data.items ?? []);
    } catch (err) {
      const e = err as { message?: string };
      notification.error({
        message: "Failed to load API tokens",
        description: e.message ?? "Unknown error",
      });
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  useEffect(() => {
    if (createOpen) {
      setAccessMode("full");
      setScopeSel({});
      setDdnsSel(false);
      setDdnsRecordId("");
    }
  }, [createOpen]);

  const onCreate = async () => {
    try {
      const values = await createForm.validateFields();
      const body: { name: string; expires_in_seconds?: number; scopes?: string[] } = {
        name: values.name,
      };
      if (values.expires_in_seconds && values.expires_in_seconds > 0) {
        body.expires_in_seconds = values.expires_in_seconds;
      }
      if (accessMode === "custom") {
        const scopes: string[] = [];
        for (const a of AREAS) {
          for (const act of scopeSel[a.key] ?? []) scopes.push(`${act}:${a.key}`);
        }
        if (ddnsSel) scopes.push("ddns");
        if (ddnsSel && ddnsRecordId.trim()) scopes.push(`record:${ddnsRecordId.trim()}`);
        if (scopes.length === 0) {
          notification.error({
            message: "Select at least one permission",
            description: "Pick some permissions, or choose Full access.",
          });
          return;
        }
        body.scopes = scopes;
      }
      const resp = await apiClient.post<CreateResponse>("/me/api-tokens", body);
      setSecret({ secret: resp.data.secret, name: resp.data.token.name });
      setCreateOpen(false);
      createForm.resetFields();
      void load();
    } catch (err) {
      // validateFields throws with a list of errors; nothing to toast.
      if ((err as { errorFields?: unknown }).errorFields) return;
      const e = err as { message?: string };
      notification.error({
        message: "Failed to create token",
        description: e.message ?? "Unknown error",
      });
    }
  };

  const onRevoke = async (t: UserAPIToken) => {
    try {
      await apiClient.delete(`/me/api-tokens/${t.id}`);
      notification.success({ message: `Token "${t.name}" revoked` });
      void load();
    } catch (err) {
      const e = err as { message?: string };
      notification.error({
        message: "Failed to revoke token",
        description: e.message ?? "Unknown error",
      });
    }
  };

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      notification.success({ message: "Copied to clipboard" });
    } catch {
      notification.error({
        message: "Copy failed",
        description: "Select the secret and copy manually.",
      });
    }
  };

  const columns: ColumnsType<UserAPIToken> = useMemo(
    () => [
      {
        title: "Name",
        dataIndex: "name",
        sorter: (a, b) => a.name.localeCompare(b.name),
        defaultSortOrder: "ascend" as const,
        render: (name: string, row) => (
          <Space>
            <KeyOutlined />
            <span>{name}</span>
            <Typography.Text type="secondary">
              (jat_…{row.secret_last4})
            </Typography.Text>
          </Space>
        ),
      },
      {
        title: "Permissions",
        render: (_: unknown, row) =>
          row.scopes && row.scopes.length > 0 ? (
            <Space size={[0, 4]} wrap>
              {row.scopes.map((sc) => (
                <Tag key={sc}>{SCOPE_LABELS[sc] ?? sc}</Tag>
              ))}
            </Space>
          ) : (
            <Tag color="blue">Full access</Tag>
          ),
      },
      {
        title: "Status",
        render: (_: unknown, row) => statusTag(row),
      },
      {
        title: "Created",
        dataIndex: "created_at",
        sorter: (a, b) => (a.created_at ? +new Date(a.created_at) : 0) - (b.created_at ? +new Date(b.created_at) : 0),
        render: fmtDate,
      },
      {
        title: "Last used",
        render: (_: unknown, row) => (
          <Tooltip
            title={
              row.last_used_ip
                ? `from ${row.last_used_ip}`
                : "no usage recorded"
            }
          >
            <span>{fmtDate(row.last_used_at)}</span>
          </Tooltip>
        ),
      },
      {
        title: "Expires",
        dataIndex: "expires_at",
        sorter: (a, b) => (a.expires_at ? +new Date(a.expires_at) : 0) - (b.expires_at ? +new Date(b.expires_at) : 0),
        render: fmtDate,
      },
      {
        title: "Actions",
        render: (_: unknown, row) =>
          row.revoked_at ? (
            <Typography.Text type="secondary">
              revoked {fmtDate(row.revoked_at)}
            </Typography.Text>
          ) : (
            <Popconfirm
              title={t("userapitokenspage.revoke_this_token")}
              description={t("userapitokenspage.any_script_using_it_will_start_getting_401")}
              okText={t("userapitokenspage.revoke")}
              okType="danger"
              onConfirm={() => onRevoke(row)}
            >
              <Button danger icon={<DeleteOutlined />} size="small">
                Revoke
              </Button>
            </Popconfirm>
          ),
      },
    ],
    [],
  );

  const tokensCard = (
    <Card
      title={t("userapitokenspage.personal_api_tokens")}
      extra={
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setCreateOpen(true)}
        >
          New token
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Tokens authenticate API requests as you, scoped to resources
        you own. Use them to script the documented{" "}
        <Typography.Text strong>Automation API</Typography.Text> — DNS
        records, WordPress deploys, a DDNS updater on your router, and the
        other endpoints listed under Automation API (a stable, documented
        subset, not every endpoint the web UI uses). Send the token as{" "}
        <Typography.Text code>Authorization: Bearer jat_…</Typography.Text>
        .
      </Typography.Paragraph>

      <Table<UserAPIToken>
        rowKey="id"
        loading={loading}
        dataSource={rows}
        columns={columns}
        pagination={false}
        scroll={{ x: "max-content" }}
      />
    </Card>
  );

  return (
    <>
      <Tabs
        defaultActiveKey="tokens"
        items={[
          { key: "tokens", label: "Tokens", children: tokensCard },
          { key: "ddns", label: "Dynamic DNS", children: <DDNSSetupGuide /> },
          { key: "mcp", label: "MCP", children: <MCPSetupGuide /> },
          { key: "docs", label: "Automation API", children: <APIDocsPage /> },
        ]}
      />

      <Modal
        title={t("userapitokenspage.create_api_token")}
        open={createOpen}
        onCancel={() => {
          setCreateOpen(false);
          createForm.resetFields();
        }}
        onOk={onCreate}
        okText={t("userapitokenspage.create")}
      >
        <Form form={createForm} layout="vertical">
          <Form.Item
            label={t("userapitokenspage.name")}
            name="name"
            rules={[
              { required: true, message: "Required" },
              { max: 100, message: "Max 100 characters" },
            ]}
            extra="A label for you — e.g. 'office router DDNS', 'CI deploy bot'."
          >
            <Input placeholder={t("userapitokenspage.my_token")} autoFocus maxLength={100} />
          </Form.Item>
          <Form.Item
            label={t("userapitokenspage.expires_in_seconds_optional")}
            name="expires_in_seconds"
            extra="Leave blank for a token that never expires. Max 1 year (31536000)."
            rules={[
              {
                type: "number",
                min: 60,
                max: 31536000,
                message: "60 to 31,536,000 (1 minute to 1 year)",
              },
            ]}
          >
            <InputNumber
              min={60}
              max={31536000}
              step={3600}
              style={{ width: "100%" }}
              placeholder="e.g. 86400 for 1 day"
            />
          </Form.Item>
          <Form.Item
            label={t("userapitokenspage.permissions")}
            tooltip={t("userapitokenspage.full_access_can_do_anything_you_can_a_custom")}
          >
            <Radio.Group
              value={accessMode}
              onChange={(e) => setAccessMode(e.target.value)}
              style={{ marginBottom: accessMode === "custom" ? 12 : 0 }}
            >
              <Radio value="full">Full access</Radio>
              <Radio value="custom">Custom</Radio>
            </Radio.Group>
            {accessMode === "custom" && (
              <div
                style={{
                  maxHeight: 280,
                  overflowY: "auto",
                  border: "1px solid #f0f0f0",
                  borderRadius: 6,
                  padding: 12,
                }}
              >
                <div style={{ marginBottom: 10 }}>
                  <Checkbox checked={ddnsSel} onChange={(e) => setDdnsSel(e.target.checked)}>
                    DDNS only — update DNS records from a router (safe for router config)
                  </Checkbox>
                  {ddnsSel && (
                    <div style={{ marginTop: 6, marginLeft: 24 }}>
                      <Input
                        size="small"
                        placeholder={t("userapitokenspage.limit_to_one_dns_record_id_optional")}
                        value={ddnsRecordId}
                        onChange={(e) => setDdnsRecordId(e.target.value)}
                        allowClear
                      />
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        Leave blank to allow updating any of your DNS records. The record ID is shown on the domain&apos;s DNS page.
                      </Typography.Text>
                    </div>
                  )}
                </div>
                {AREAS.map((a) => (
                  <div
                    key={a.key}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      justifyContent: "space-between",
                      marginBottom: 6,
                    }}
                  >
                    <span style={{ width: 120 }}>{a.label}</span>
                    <Checkbox.Group
                      options={[
                        { label: "Read", value: "read" },
                        { label: "Write", value: "write" },
                      ]}
                      value={scopeSel[a.key] ?? []}
                      onChange={(v) =>
                        setScopeSel((prev) => ({ ...prev, [a.key]: v as string[] }))
                      }
                    />
                  </div>
                ))}
              </div>
            )}
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={`Token created: ${secret?.name ?? ""}`}
        open={!!secret}
        onCancel={() => setSecret(null)}
        onOk={() => setSecret(null)}
        okText={t("userapitokenspage.i_ve_copied_it")}
        cancelButtonProps={{ style: { display: "none" } }}
        closable={false}
        maskClosable={false}
      >
        <Alert
          type="warning"
          showIcon
          message={t("userapitokenspage.copy_this_secret_now")}
          description={t("userapitokenspage.the_plaintext_token_is_shown_only_once_after")}
          style={{ marginBottom: 16 }}
        />
        <Input.Group compact>
          <Input
            value={secret?.secret}
            readOnly
            onFocus={(e) => e.currentTarget.select()}
            style={{ width: "calc(100% - 90px)" }}
          />
          <Button
            type="primary"
            icon={<CopyOutlined />}
            onClick={() => secret && void copy(secret.secret)}
            style={{ width: 90 }}
          >
            Copy
          </Button>
        </Input.Group>
      </Modal>
    </>
  );
}

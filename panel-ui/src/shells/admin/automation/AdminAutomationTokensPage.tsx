// AdminAutomationTokensPage — admin Automation API token list +
// mint Drawer + one-time-secret reveal Modal (M44).
//
// Tokens are HMAC bearers external automations use to hit the
// /api/v1/automation/* read-only surface. Plaintext secret is
// exposed exactly once — at mint time — via a Modal with a
// copy-to-clipboard button and a "save it now" warning.
//
// Revocation is soft (sets revoked_at). Operators can audit which
// admin minted what + when it was last used.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { StandardDrawerFooter } from "../../../components/StandardActionFooter";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Drawer,
  Form,
  Input,
  Modal,
  Popconfirm,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
  theme,
} from "antd";
import { KeyOutlined, PlusOutlined, DeleteOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

type Token = {
  id: string;
  name: string;
  scopes: string[];
  created_at: string;
  last_used_at?: string | null;
  last_used_ip?: string | null;
  revoked_at?: string | null;
};

type ListResp = { data: Token[]; total: number };
type MintResp = Token & { secret: string };

const SCOPE_OPTIONS = [
  { value: "read:*", label: "read:* (everything below)" },
  { value: "read:domains", label: "read:domains" },
  { value: "read:users", label: "read:users" },
  { value: "read:applications", label: "read:applications" },
  { value: "read:status", label: "read:status" },
  // JAB-140 write scopes. A write token acts on ANY tenant — grant sparingly.
  { value: "write:*", label: "write:* (all write actions)" },
  { value: "write:services", label: "write:services (restart)" },
  { value: "write:users", label: "write:users (disable/enable)" },
  { value: "write:domains", label: "write:domains (suspend/unsuspend)" },
  { value: "write:cache", label: "write:cache (purge)" },
  { value: "write:backups", label: "write:backups (trigger backup)" },
];

// Wildcard families that are mutually exclusive with their own explicit children.
const SCOPE_WILDCARDS = ["read:*", "write:*"] as const;
function famPrefix(wildcard: string): string {
  return wildcard.slice(0, wildcard.length - 1); // "read:*" -> "read:"
}

function fmt(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export const AdminAutomationTokensPage = () => {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const qc = useQueryClient();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [revealSecret, setRevealSecret] = useState<string | null>(null);
  const [revealName, setRevealName] = useState<string | null>(null);
  // JAB-86: surface the token ID alongside the secret at mint time so the
  // operator can wire both into automation config without reopening the list.
  const [revealId, setRevealId] = useState<string | null>(null);
  const [form] = Form.useForm<{ name: string; scopes: string[]; writes_enabled: boolean }>();
  const scopeVals = (Form.useWatch("scopes", form) as string[] | undefined) ?? [];
  const hasWriteScope = scopeVals.some((s) => s.startsWith("write:"));
  // JAB-84 + JAB-140: within each family (read:*, write:*) the wildcard is
  // mutually exclusive with its explicit children. Picking a wildcard collapses
  // to just it; picking a child in that family clears the wildcard.
  const normalizeScopes = (checked: string[]): string[] => {
    const prev = (form.getFieldValue("scopes") as string[] | undefined) ?? [];
    let out = checked;
    for (const w of SCOPE_WILDCARDS) {
      const pfx = famPrefix(w);
      if (out.includes(w) && !prev.includes(w)) {
        // Just picked this wildcard → drop the family's explicit children.
        out = out.filter((x) => x === w || !x.startsWith(pfx));
      } else if (out.some((x) => x !== w && x.startsWith(pfx) && !prev.includes(x))) {
        // Just picked a child in this family → drop the wildcard.
        out = out.filter((x) => x !== w);
      }
    }
    return out;
  };
  const scopeOptions = SCOPE_OPTIONS.map((o) => {
    if (o.value.endsWith(":*")) return o;
    const wildcard = SCOPE_WILDCARDS.find((w) => o.value.startsWith(famPrefix(w)));
    return { ...o, disabled: wildcard ? scopeVals.includes(wildcard) : false };
  });

  const list = useQuery<ListResp>({
    queryKey: ["list", "admin/automation/tokens"],
    queryFn: async () => {
      const { data } = await apiClient.get<ListResp>("/admin/automation/tokens");
      return data;
    },
  });

  const mint = useMutation<MintResp, unknown, { name: string; scopes: string[]; writes_enabled?: boolean }>({
    mutationFn: async (input) => {
      const { data } = await apiClient.post<MintResp>("/admin/automation/tokens", input);
      return data;
    },
    onSuccess: async (resp) => {
      await qc.invalidateQueries({ queryKey: ["list", "admin/automation/tokens"] });
      setDrawerOpen(false);
      form.resetFields();
      setRevealName(resp.name);
      setRevealId(resp.id);
      setRevealSecret(resp.secret);
    },
  });

  const revoke = useMutation<unknown, unknown, { id: string }>({
    mutationFn: async ({ id }) => {
      await apiClient.delete(`/admin/automation/tokens/${id}`);
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["list", "admin/automation/tokens"] });
    },
  });

  const handleMint = async (values: { name: string; scopes: string[]; writes_enabled?: boolean }) => {
    try {
      await mint.mutateAsync(values);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Mint failed");
    }
  };

  const copySecret = async () => {
    if (!revealSecret) return;
    try {
      await navigator.clipboard.writeText(revealSecret);
      message.success("Secret copied to clipboard");
    } catch {
      message.error("Clipboard access blocked — copy manually");
    }
  };

  return (
    <div>
      <Typography.Title level={2}>
        <Space>
          <KeyOutlined /> Server API Access
        </Space>
      </Typography.Title>
      <Typography.Paragraph type="secondary">
        HMAC-signed bearer tokens for external automations (CI scripts, monitoring,
        partner integrations). Every request signs <code>METHOD || PATH || TS || SHA256(BODY)</code>{" "}
        with the per-token secret. Tokens are scoped — issue the narrowest scope set the caller
        needs.
      </Typography.Paragraph>

      <Card
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setDrawerOpen(true)}
          >
            Mint Token
          </Button>
        }
      >
        <Table<Token>
          rowKey="id"
          loading={list.isLoading}
          dataSource={list.data?.data ?? []}
          pagination={false}
          scroll={{ x: "max-content" }}
        >
          <Table.Column<Token> title={t("adminautomationtokenspage.name")} dataIndex="name" />
          <Table.Column<Token>
            title={t("adminautomationtokenspage.token_id")}
            dataIndex="id"
            render={(v: string) => (
              <Typography.Text copyable code style={{ fontSize: 12 }}>
                {v}
              </Typography.Text>
            )}
          />
          <Table.Column<Token>
            title={t("adminautomationtokenspage.scopes")}
            width={260}
            render={(_, r) => (
              <Space wrap size={4} style={{ maxWidth: 260 }}>
                {r.scopes.map((s) => (
                  <Tag key={s} color={s === "read:*" ? "purple" : "blue"} style={{ marginInlineEnd: 0 }}>
                    {s}
                  </Tag>
                ))}
              </Space>
            )}
          />
          <Table.Column<Token>
            title={t("adminautomationtokenspage.created")}
            dataIndex="created_at"
            render={(v: string) => fmt(v)}
          />
          <Table.Column<Token>
            title={t("adminautomationtokenspage.last_used")}
            render={(_, r) => (
              <Space direction="vertical" size={0}>
                <span>{fmt(r.last_used_at)}</span>
                {r.last_used_ip && (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {r.last_used_ip}
                  </Typography.Text>
                )}
              </Space>
            )}
          />
          <Table.Column<Token>
            title={t("adminautomationtokenspage.status")}
            render={(_, r) =>
              r.revoked_at ? (
                <Tag color="red">revoked {fmt(r.revoked_at)}</Tag>
              ) : (
                <Tag color="green">active</Tag>
              )
            }
          />
          <Table.Column<Token>
            title={t("adminautomationtokenspage.actions")}
            render={(_, r) =>
              r.revoked_at ? (
                <Typography.Text type="secondary">—</Typography.Text>
              ) : (
                <Popconfirm
                  title={`Revoke token "${r.name}"?`}
                  description={t("adminautomationtokenspage.external_callers_using_this_token_will_start")}
                  okText={t("adminautomationtokenspage.revoke")}
                  okButtonProps={{ danger: true }}
                  onConfirm={() => revoke.mutateAsync({ id: r.id })}
                >
                  <Button danger icon={<DeleteOutlined />} variant="filled" color="danger">
                    Revoke
                  </Button>
                </Popconfirm>
              )
            }
          />
        </Table>
      </Card>

      <Drawer
        title={t("adminautomationtokenspage.mint_server_api_token")}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        width={500}
        destroyOnClose
        footer={
          <StandardDrawerFooter
            formId="mint-token-form"
            primaryText="Mint"
            primaryLoading={mint.isPending}
            onCancel={() => setDrawerOpen(false)}
          />
        }
      >
        <Form
          id="mint-token-form"
          form={form}
          layout="vertical"
          onFinish={handleMint}
          initialValues={{ scopes: ["read:status"] }}
        >
          <Form.Item
            name="name"
            label={t("adminautomationtokenspage.name")}
            rules={[
              { required: true, message: "Required" },
              { max: 100 },
            ]}
            extra="Human-readable label. Tokens are unique by name."
          >
            <Input placeholder="e.g. monitoring-bot, ci-deploy" />
          </Form.Item>

          <Form.Item
            name="scopes"
            label={t("adminautomationtokenspage.scopes")}
            rules={[{ required: true, message: "At least one scope" }]}
            getValueFromEvent={normalizeScopes}
            extra="Wildcard 'read:*' grants every read; otherwise tick only the resources the automation needs (mutually exclusive)."
          >
            <Checkbox.Group options={scopeOptions} style={{ display: "flex", flexDirection: "column", gap: 8 }} />
          </Form.Item>

          {hasWriteScope ? (
            <Form.Item
              name="writes_enabled"
              label={t("adminautomationtokenspage.writes_enabled")}
              valuePropName="checked"
              initialValue={true}
              extra="Master switch. Turn off to mint a write-scoped token with writes paused — it can be enabled later without re-issuing the secret. A write token acts on any tenant; grant sparingly."
            >
              <Switch />
            </Form.Item>
          ) : null}
        </Form>
      </Drawer>

      <Modal
        open={revealSecret !== null}
        title={`Token "${revealName}" minted`}
        closable={false}
        footer={[
          <Button key="copy" type="primary" onClick={copySecret}>
            Copy to clipboard
          </Button>,
          <Button
            key="done"
            onClick={() => {
              setRevealSecret(null);
              setRevealName(null);
              setRevealId(null);
            }}
          >
            I've saved it
          </Button>,
        ]}
        width={600}
      >
        <Typography.Paragraph style={{ marginBottom: 12 }}>
          <Typography.Text type="secondary">Token ID</Typography.Text>
          <Typography.Paragraph copyable={{ text: revealId ?? "", tooltips: ["Copy", "Copied"] }} style={{ marginBottom: 0 }}>
            <code style={{ wordBreak: "break-all", display: "block", padding: 12, background: token.colorBgLayout, border: `1px solid ${token.colorBorder}`, borderRadius: token.borderRadius }}>
              {revealId}
            </code>
          </Typography.Paragraph>
        </Typography.Paragraph>
        <Alert
          type="warning"
          showIcon
          message={t("adminautomationtokenspage.this_is_the_only_time_the_secret_will_be_sho")}
          description={t("adminautomationtokenspage.copy_it_now_and_store_it_in_your_automation")}
          style={{ marginBottom: 16 }}
        />
        <Typography.Text type="secondary">Secret</Typography.Text>
        <Typography.Paragraph copyable={{ text: revealSecret ?? "", tooltips: ["Copy", "Copied"] }}>
          <code style={{ wordBreak: "break-all", display: "block", padding: 12, background: token.colorBgLayout, border: `1px solid ${token.colorBorder}`, borderRadius: token.borderRadius }}>
            {revealSecret}
          </code>
        </Typography.Paragraph>
      </Modal>

    </div>
  );
};

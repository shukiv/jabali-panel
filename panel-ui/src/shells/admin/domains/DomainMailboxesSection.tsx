// DomainMailboxesSection — list + CRUD for mailboxes within a domain.
//
// Renders as a Card beneath the Email section on DomainEdit. The
// section is only useful once email is enabled (the panel-API returns
// 409 email_not_enabled on create otherwise), so the empty-state
// explains that. Reveal-once passwords reuse DatabaseUserPasswordModal
// — the UX is identical (operator saves, we never display again).
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { RowActions } from "../../../components/RowActions";
import { Alert, Button, Card, Form, Input, Checkbox, InputNumber, Modal, Select, Skeleton, Space, Table, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  DeleteOutlined,
  KeyOutlined,
  MailOutlined,
  PlusOutlined,
} from "@icons";

import { DatabaseUserPasswordModal } from "../../../components/DatabaseUserPasswordModal";
import { PasswordInput } from "../../../components/PasswordInput";
import {
  renderMailboxQuota,
  renderMailboxStatus,
  useMailboxWebmail,
} from "../../../components/mail/mailboxInventory";
import {
  useCreateMailbox,
  useDeleteMailbox,
  useDomainEmail,
  useEnableDomainEmail,
  useMailboxes,
  useRotateMailboxPassword,
  type Mailbox,
} from "../../../hooks/useMailboxes";

type Props = {
  domainId: string;
  // domainOptions — when provided, the Create dialog renders a Domain
  // selector seeded with these choices. Callers pass the list of
  // email-enabled domains the user owns so they can create a mailbox on
  // any of them without first switching the page's top-level selector.
  // When omitted (e.g. admin DomainEdit, which is intentionally scoped
  // to a single domain), the dialog keeps its single-domain behaviour.
  domainOptions?: Array<{ id: string; name: string }>;
  // onDomainCreated — called after a successful create to let the host
  // page refocus on the newly-used domain (e.g. switch its top-level
  // selector so the operator lands on the list containing the new row).
  onDomainCreated?: (domainId: string) => void;
};

// Quota presets in bytes — 1 GiB default matches the DB column default;
// the 16 MiB floor is panel-api's minMailboxQuotaBytes.
const QUOTA_DEFAULT_BYTES = 1 * 1024 * 1024 * 1024;
const QUOTA_MIN_BYTES = 16 * 1024 * 1024;

function parseQuotaInput(v: number | string | null | undefined): number | undefined {
  if (v === null || v === undefined || v === "") return undefined;
  const n = typeof v === "number" ? v : Number(v);
  if (Number.isNaN(n) || n <= 0) return undefined;
  return Math.floor(n * 1024 * 1024); // UI unit is MiB, wire unit is bytes
}

export const DomainMailboxesSection = ({
  domainId,
  domainOptions,
  onDomainCreated,
}: Props) => {
  const { t } = useTranslation();
  const email = useDomainEmail(domainId);
  const enabled = email.data?.email_enabled ?? false;

  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const list = useMailboxes({
    domainId,
    params: { page, pageSize, sort: "local_part", order: "asc" },
    enabled,
  });

  const createMutation = useCreateMailbox();
  const deleteMutation = useDeleteMailbox();
  const rotateMutation = useRotateMailboxPassword();
  const webmail = useMailboxWebmail();
  // enableMutation is only used when the section lands in the
  // disabled state (auto-enable failed at domain.create time, or an
  // operator explicitly turned email off). In the happy path — email
  // auto-enabled on domain create — the button never mounts.
  const enableMutation = useEnableDomainEmail();

  const [createOpen, setCreateOpen] = useState(false);
  const [passwordModal, setPasswordModal] = useState<{
    email: string;
    password: string;
    title: string;
  } | null>(null);
  const [rotatingId, setRotatingId] = useState<string | null>(null);
  const [form] = Form.useForm<{
    domain_id: string;
    local_part: string;
    password?: string;
    quota_mib?: number;
    send_only?: boolean;
  }>();

  // Only render the in-dialog domain picker when the caller actually
  // supplied options AND there's a real choice to make. A list of one
  // collapses to the same single-domain UX as before.
  const showDomainPicker = (domainOptions?.length ?? 0) > 1;

  // The form's `domain_id` field is seeded to the section's domainId
  // each time the dialog opens. When showDomainPicker is false the
  // field is never rendered; we still use this value on submit so the
  // create call always has a target domain.
  const currentFormDomainId =
    Form.useWatch("domain_id", form) ?? domainId;
  const currentDomainName =
    domainOptions?.find((d) => d.id === currentFormDomainId)?.name ??
    email.data?.domain_name ??
    "";

  if (email.isLoading) {
    return <Skeleton active paragraph={{ rows: 2 }} />;
  }

  if (!enabled) {
    const onEnable = async () => {
      try {
        await enableMutation.mutateAsync({ domainId });
        feedback.message.success("Email enabled");
      } catch (err) {
        const resp = (err as { response?: { data?: { error?: string; detail?: string } } })
          ?.response?.data;
        feedback.message.error(resp?.detail ?? resp?.error ?? "Failed to enable email");
      }
    };
    return (
      <Alert
        type="info"
        showIcon
        message={t("domainmailboxessection.email_isn_t_enabled_on_this_domain")}
        description={t("domainmailboxessection.mailboxes_can_only_be_created_once_email_is")}
        action={
          <Button
            type="primary"
            size="small"
            onClick={onEnable}
            loading={enableMutation.isPending}
          >
            Enable email
          </Button>
        }
      />
    );
  }

  const rotate = async (row: Mailbox) => {
    setRotatingId(row.id);
    try {
      const resp = await rotateMutation.mutateAsync({ id: row.id });
      if (resp.password) {
        setPasswordModal({
          email: row.email,
          password: resp.password,
          title: "New mailbox password (rotation)",
        });
      } else {
        feedback.message.success("Password rotated");
      }
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        "Failed to rotate password";
      feedback.message.error(msg);
    } finally {
      setRotatingId(null);
    }
  };

  const onCreate = async () => {
    const values = await form.validateFields();
    // Fall back to the section's bound domain when the picker wasn't
    // shown (single-domain contexts like admin DomainEdit).
    const targetDomainId = values.domain_id || domainId;
    try {
      const resp = await createMutation.mutateAsync({
        domainId: targetDomainId,
        input: {
          local_part: values.local_part,
          password: values.password || undefined,
          quota_bytes: parseQuotaInput(values.quota_mib),
          send_only: values.send_only || undefined,
        },
      });
      setCreateOpen(false);
      form.resetFields();
      // If the operator created on a domain different from the one
      // currently in view, let the host page switch to it so the
      // freshly-created row is visible in the table below.
      if (targetDomainId !== domainId) {
        onDomainCreated?.(targetDomainId);
      }
      if (resp.password) {
        setPasswordModal({
          email: resp.email,
          password: resp.password,
          title: "New mailbox password",
        });
      } else {
        feedback.message.success(`Mailbox ${resp.email} created`);
      }
    } catch (err) {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })
        ?.response?.data;
      feedback.message.error(resp?.detail ?? resp?.error ?? "Failed to create mailbox");
    }
  };

  return (
    <>
      <Space
        style={{ width: "100%", justifyContent: "space-between", marginBottom: 12 }}
      >
        <Typography.Text type="secondary">
          {list.total} mailbox{list.total === 1 ? "" : "es"}
        </Typography.Text>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            // Seed the form including the current domain so the picker
            // (if rendered) defaults to the domain the user's looking at.
            form.resetFields();
            form.setFieldsValue({ domain_id: domainId });
            setCreateOpen(true);
          }}
        >
          Create mailbox
        </Button>
      </Space>

      <Card size="small" bodyStyle={{ padding: 0 }}>
        <Table<Mailbox>
          rowKey="id"
          loading={list.isLoading}
          dataSource={list.items}
          scroll={{ x: "max-content" }}
          pagination={{
            current: page,
            pageSize,
            total: list.total,
            onChange: (p, s) => {
              setPage(p);
              setPageSize(s);
            },
          }}
          columns={[
            {
              title: "Email",
              dataIndex: "email",
              render: (v: string) => (
                <Typography.Text style={{ fontFamily: "monospace" }}>{v}</Typography.Text>
              ),
            },
            {
              title: "Quota",
              dataIndex: "quota_bytes",
              width: 220,
              render: (_quota: number, row: Mailbox) => renderMailboxQuota(row),
            },
            {
              title: "Last usage",
              dataIndex: "last_usage_at",
              width: 140,
              render: (v: string | null) =>
                v ? new Date(v).toLocaleString() : (
                  <Typography.Text type="secondary">never</Typography.Text>
                ),
            },
            {
              title: "Status",
              dataIndex: "is_disabled",
              width: 100,
              render: (disabled: boolean, row: Mailbox) => (
                // Domain adapter composes the shared active/disabled tag with its
                // own send-only marker (GH #371) — kept explicit per JAB-333.
                <Space size={4} wrap>
                  {renderMailboxStatus(disabled)}
                  {row.send_only && <Tag color="blue">send-only</Tag>}
                </Space>
              ),
            },
            {
              title: "Actions",
              width: 180,
              render: (_, row) => (
                <RowActions
                  actions={[
                    { key: "webmail", label: "Open webmail", icon: <MailOutlined />, loading: webmail.isLaunching(row.id), onClick: () => webmail.launch(row.id) },
                    { key: "rotate", label: "Rotate password", icon: <KeyOutlined />, loading: rotatingId === row.id, onClick: () => rotate(row) },
                    {
                      key: "delete",
                      label: "Remove",
                      icon: <DeleteOutlined />,
                      danger: true,
                      onClick: async () => {
                        try {
                          await deleteMutation.mutateAsync({ id: row.id, domainId });
                          feedback.message.success("Mailbox deleted");
                        } catch (err) {
                          const msg = (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail ?? "Failed to delete";
                          feedback.message.error(msg);
                        }
                      },
                      confirm: { title: `Delete ${row.email}?`, description: "All mail in this mailbox will be removed. This cannot be undone.", okText: "Delete" },
                    },
                  ]}
                />
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title={t("domainmailboxessection.create_mailbox")}
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={onCreate}
        confirmLoading={createMutation.isPending}
        okText={t("domainmailboxessection.create")}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          {showDomainPicker && (
            <Form.Item
              label={t("domainmailboxessection.domain")}
              name="domain_id"
              rules={[{ required: true, message: "Pick a domain" }]}
              tooltip={t("domainmailboxessection.only_domains_with_email_enabled_appear_here")}
            >
              <Select
                showSearch
                optionFilterProp="label"
                options={(domainOptions ?? []).map((d) => ({
                  value: d.id,
                  label: d.name,
                }))}
                placeholder={t("domainmailboxessection.pick_a_domain")}
              />
            </Form.Item>
          )}
          <Form.Item
            label={t("domainmailboxessection.local_part")}
            name="local_part"
            rules={[
              { required: true, message: "Required" },
              {
                pattern: /^[a-z0-9][a-z0-9._+-]*$/i,
                message: "Letters, digits, dot/underscore/plus/hyphen only",
              },
              { max: 64, message: "64 characters max" },
            ]}
            tooltip={`Full address will be <local>@${currentDomainName}`}
          >
            <Input
              placeholder="alice"
              autoComplete="off"
              addonAfter={`@${currentDomainName}`}
            />
          </Form.Item>

          <Form.Item
            label={t("domainmailboxessection.password")}
            name="password"
            tooltip={t("domainmailboxessection.leave_blank_to_auto_generate_generated_passw")}
            rules={[
              { min: 8, message: "8 characters minimum" },
            ]}
          >
            <PasswordInput autoComplete="new-password" placeholder="(auto-generate)" />
          </Form.Item>

          <Form.Item
            label={t("domainmailboxessection.quota_mib")}
            name="quota_mib"
            tooltip={`Default 1024 MiB. Minimum ${QUOTA_MIN_BYTES / 1024 / 1024} MiB.`}
            initialValue={QUOTA_DEFAULT_BYTES / 1024 / 1024}
          >
            <InputNumber
              min={QUOTA_MIN_BYTES / 1024 / 1024}
              max={1024 * 1024}
              step={256}
              style={{ width: 200 }}
            />
          </Form.Item>
          <Form.Item
            name="send_only"
            valuePropName="checked"
            tooltip={t("domainmailboxessection.a_send_only_mailbox_authenticates_for_smtp_s")}
          >
            <Checkbox>Send-only (SMTP submission, no inbox)</Checkbox>
          </Form.Item>
        </Form>
      </Modal>

      <DatabaseUserPasswordModal
        open={passwordModal !== null}
        username={passwordModal?.email ?? ""}
        password={passwordModal?.password ?? ""}
        title={passwordModal?.title}
        onClose={() => setPasswordModal(null)}
      />
    </>
  );
};

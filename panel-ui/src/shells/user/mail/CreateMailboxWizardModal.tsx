// CreateMailboxWizardModal — 2-step mailbox creation flow.
//
// Step 1: just the Domain select. Submit is disabled until a domain is
//         picked. No other fields visible so first-time users aren't
//         hit with a wall of inputs before the target domain is chosen.
// Step 2: once a domain is selected, the rest of the form materialises
//         (local part, password, quota). Same submit button; now
//         enabled once local_part is filled.
//
// The modal calls POST /domains/:id/mailboxes on submit and surfaces
// the reveal-once password via the onCreated callback — the parent
// page pops the DatabaseUserPasswordModal with it.
import { Alert, Button, Checkbox, Drawer, Form, Grid, Input, InputNumber, Select, Space } from "antd";

import { PasswordInput } from "../../../components/PasswordInput";
import { useMailGroups, useAddMailboxToGroup } from "../../../hooks/useMailGroups";
import { useCreateForwarder } from "../../../hooks/useForwarders";
import {
  useCreateMailbox,
  type CreateMailboxResponse,
} from "../../../hooks/useMailboxes";

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

type DomainOption = { id: string; name: string };

type Props = {
  open: boolean;
  domains: DomainOption[];
  onCancel: () => void;
  onCreated: (resp: CreateMailboxResponse) => void;
};

type FormValues = {
  domain_id?: string;
  local_part?: string;
  display_name?: string;
  password?: string;
  quota_mib?: number;
  send_only?: boolean;
  group_ids?: string[];
  aliases?: string;
  forward_target?: string;
  forward_keep_copy?: boolean;
};

export const CreateMailboxWizardModal = ({
  open,
  domains,
  onCancel,
  onCreated,
}: Props) => {
  const [form] = Form.useForm<FormValues>();
  const createMutation = useCreateMailbox();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  // Watching domain_id is what drives step-2 reveal. Form.useWatch
  // returns undefined until the user opens the Select and picks a
  // value, so the cascading fields stay hidden on first render.
  const watchedDomainId = Form.useWatch("domain_id", form);
  const chosenDomain = domains.find((d) => d.id === watchedDomainId);
  const { data: domainGroups } = useMailGroups(watchedDomainId);
  const addToGroup = useAddMailboxToGroup();
  const createForwarder = useCreateForwarder();

  const onOk = async () => {
    // validateFields rejects when required rules fail; AntD surfaces
    // the per-field error text inline, so we just swallow the reject
    // here and let the user correct and retry.
    let values: FormValues;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    if (!values.domain_id || !values.local_part) return;
    try {
      const resp = await createMutation.mutateAsync({
        domainId: values.domain_id,
        input: {
          local_part: values.local_part,
          display_name: values.display_name?.trim() || undefined,
          password: values.password || undefined,
          quota_bytes: parseQuotaInput(values.quota_mib),
          send_only: values.send_only || undefined,
        },
      });
      if (values.group_ids?.length) {
        await Promise.all(
          values.group_ids.map((gid) =>
            addToGroup
              .mutateAsync({ groupId: gid, mailboxId: resp.id, domainId: values.domain_id })
              .catch(() => {}),
          ),
        );
      }

      // GH #237: create aliases + an optional external forward at create time.
      const mbEmail = `${values.local_part}@${chosenDomain?.name ?? ""}`;
      const aliasParts = (values.aliases ?? "")
        .split(/[\n,]/)
        .map((a) => a.trim().toLowerCase())
        .filter((a) => a.length > 0);
      await Promise.all(
        aliasParts.map((localPart) =>
          createForwarder
            .mutateAsync({ mailboxID: resp.id, type: "alias", localPart, target: mbEmail })
            .catch(() => {}),
        ),
      );
      const fwd = values.forward_target?.trim();
      if (fwd) {
        await createForwarder
          .mutateAsync({
            mailboxID: resp.id,
            type: "external",
            target: fwd,
            keepCopy: values.forward_keep_copy ?? false,
          })
          .catch(() => {});
      }
      form.resetFields();
      onCreated(resp);
    } catch (err) {
      const detail =
        (err as { response?: { data?: { detail?: string; error?: string } } })?.response
          ?.data?.detail ??
        (err as { response?: { data?: { detail?: string; error?: string } } })?.response
          ?.data?.error;
      // Surface validation/conflict errors as a form-level alert rather
      // than a floating toast so the user sees them next to the inputs
      // that caused them.
      form.setFields([
        {
          name: "local_part",
          errors: [detail ?? "Failed to create mailbox"],
        },
      ]);
    }
  };

  const onCancelInternal = () => {
    form.resetFields();
    onCancel();
  };

  return (
    <Drawer
      title="Create mailbox"
      open={open}
      onClose={onCancelInternal}
      width={isDesktop ? 520 : undefined}
      placement="right"
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onCancelInternal}>Cancel</Button>
          <Button type="primary" loading={createMutation.isPending} onClick={onOk}>
            Create mailbox
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        initialValues={{ quota_mib: QUOTA_DEFAULT_BYTES / 1024 / 1024 }}
      >
        <Form.Item
          label="Domain"
          name="domain_id"
          rules={[{ required: true, message: "Pick a domain" }]}
          tooltip="Only domains with email enabled appear here."
        >
          <Select
            showSearch
            optionFilterProp="label"
            placeholder="Select a domain"
            options={domains.map((d) => ({ value: d.id, label: d.name }))}
          />
        </Form.Item>

        {chosenDomain && (
          <>
            <Form.Item
              label="Email address"
              name="local_part"
              rules={[
                { required: true, message: "Required" },
                {
                  pattern: /^[a-z0-9][a-z0-9._+-]*$/i,
                  message: "Letters, digits, dot/underscore/plus/hyphen only",
                },
                { max: 64, message: "64 characters max" },
              ]}
              extra="The part before the @ symbol."
            >
              <Input
                placeholder="alice"
                autoComplete="off"
                addonAfter={`@${chosenDomain.name}`}
              />
            </Form.Item>

            <Form.Item
              label="Display name"
              name="display_name"
              tooltip="Shown as the sender name in webmail and outgoing mail (e.g. Alice Smith). Optional."
            >
              <Input placeholder="Alice Smith" autoComplete="off" maxLength={255} />
            </Form.Item>

            <Form.Item
              label="Password"
              name="password"
              tooltip="Leave blank to auto-generate. Generated passwords are shown exactly once."
              rules={[{ min: 8, message: "8 characters minimum" }]}
            >
              <PasswordInput
                autoComplete="new-password"
                placeholder="(auto-generate)"
              />
            </Form.Item>

            <Form.Item
              label="Quota (MiB)"
              name="quota_mib"
              tooltip={`Default ${QUOTA_DEFAULT_BYTES / 1024 / 1024} MiB. Minimum ${
                QUOTA_MIN_BYTES / 1024 / 1024
              } MiB.`}
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
              tooltip="A send-only mailbox can authenticate for SMTP submission (sending) but never receives or stores mail — inbound is rejected. Ideal for per-service or per-appliance notification credentials, so each system has its own account to revoke or rotate."
            >
              <Checkbox>Send-only (SMTP submission, no inbox)</Checkbox>
            </Form.Item>

            {domainGroups && domainGroups.length > 0 && (
              <Form.Item
                label="Add to groups"
                name="group_ids"
                tooltip="The mailbox joins these groups and gains their shared mailbox, calendar, address book and files."
              >
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="Select groups (optional)"
                  optionFilterProp="label"
                  options={domainGroups.map((g) => ({ value: g.id, label: g.display_name || g.email }))}
                />
              </Form.Item>
            )}

            <Form.Item
              label="Aliases (optional)"
              name="aliases"
              tooltip="Extra addresses that deliver to this mailbox. One local-part per line or comma-separated — e.g. sales, info."
            >
              <Input.TextArea
                rows={2}
                placeholder={chosenDomain ? `sales, info  (→ @${chosenDomain.name})` : "sales, info"}
                autoComplete="off"
              />
            </Form.Item>
            <Form.Item
              label="Forward to (optional)"
              name="forward_target"
              tooltip="Redirect a copy of incoming mail to an external address."
              rules={[{ type: "email", message: "Must be a valid email" }]}
            >
              <Input placeholder="external@example.com" autoComplete="off" />
            </Form.Item>
            <Form.Item name="forward_keep_copy" valuePropName="checked" noStyle>
              <Checkbox>Keep a copy in this mailbox when forwarding</Checkbox>
            </Form.Item>
          </>
        )}

        {!chosenDomain && domains.length === 0 && (
          <Alert
            type="warning"
            showIcon
            message="No email-enabled domains"
            description="Enable email on at least one domain before creating a mailbox."
          />
        )}
      </Form>
    </Drawer>
  );
};

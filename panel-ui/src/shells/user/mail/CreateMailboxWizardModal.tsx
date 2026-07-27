// CreateMailboxWizardModal — 2-step mailbox creation flow.
//
// Step 1: just the Domain select. Submit is disabled until a domain is
//         picked. No other fields visible so first-time users aren't
//         hit with a wall of inputs before the target domain is chosen.
// Step 2: once a domain is selected, the rest of the form materialises
//         (local part, password, quota). Same submit button; now
//         enabled once local_part is filled.
//
// GH #529: split into Account / Forwarding / Groups / Send As tabs to match
// the Edit Mailbox drawer so Add and Edit look and feel the same. The account
// fields still cascade in only after a domain is chosen (step-1/step-2). Send
// As is post-create only (it needs the new mailbox's id), so on Add that tab
// points the operator at Edit → Send As instead of a control that can't persist.
//
// The modal calls POST /domains/:id/mailboxes on submit and surfaces
// the reveal-once password via the onCreated callback — the parent
// page pops the DatabaseUserPasswordModal with it.
import { useTranslation } from "react-i18next";
import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Form,
  Grid,
  Input,
  InputNumber,
  Select,
  Space,
  Tabs,
  Typography,
} from "antd";

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
  const { t } = useTranslation();
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

  // Shown on the Forwarding/Groups tabs before a domain is chosen — those
  // fields depend on the selected domain (groups are per-domain), so they
  // stay gated behind the step-1 domain pick like the account fields.
  const pickDomainFirst = (
    <Typography.Text type="secondary">
      Select a domain on the Account tab first.
    </Typography.Text>
  );

  const accountTab = (
    <>
      <Form.Item
        label={t("createmailboxwizardmodal.domain")}
        name="domain_id"
        rules={[{ required: true, message: "Pick a domain" }]}
        tooltip={t("createmailboxwizardmodal.only_domains_with_email_enabled_appear_here")}
      >
        <Select
          showSearch
          optionFilterProp="label"
          placeholder={t("createmailboxwizardmodal.select_a_domain")}
          options={domains.map((d) => ({ value: d.id, label: d.name }))}
        />
      </Form.Item>

      {chosenDomain && (
        <>
          <Form.Item
            label={t("createmailboxwizardmodal.email_address")}
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
            label={t("createmailboxwizardmodal.display_name")}
            name="display_name"
            tooltip={t("createmailboxwizardmodal.shown_as_the_sender_name_in_webmail_and_outg")}
          >
            <Input placeholder={t("createmailboxwizardmodal.alice_smith")} autoComplete="off" maxLength={255} />
          </Form.Item>

          <Form.Item
            label={t("createmailboxwizardmodal.password")}
            name="password"
            tooltip={t("createmailboxwizardmodal.leave_blank_to_auto_generate_generated_passw")}
            rules={[{ min: 8, message: "8 characters minimum" }]}
          >
            <PasswordInput
              autoComplete="new-password"
              placeholder="(auto-generate)"
            />
          </Form.Item>

          <Form.Item
            label={t("createmailboxwizardmodal.quota_mib")}
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
            tooltip={t("createmailboxwizardmodal.a_send_only_mailbox_can_authenticate_for_smt")}
          >
            <Checkbox>Send-only (SMTP submission, no inbox)</Checkbox>
          </Form.Item>
        </>
      )}

      {!chosenDomain && domains.length === 0 && (
        <Alert
          type="warning"
          showIcon
          message={t("createmailboxwizardmodal.no_email_enabled_domains")}
          description={t("createmailboxwizardmodal.enable_email_on_at_least_one_domain_before_c")}
        />
      )}
    </>
  );

  const forwardingTab = chosenDomain ? (
    <>
      <Form.Item
        label={t("createmailboxwizardmodal.aliases_optional")}
        name="aliases"
        tooltip={t("createmailboxwizardmodal.extra_addresses_that_deliver_to_this_mailbox")}
      >
        <Input.TextArea
          rows={2}
          placeholder={`sales, info  (→ @${chosenDomain.name})`}
          autoComplete="off"
        />
      </Form.Item>
      <Form.Item
        label={t("createmailboxwizardmodal.forward_to_optional")}
        name="forward_target"
        tooltip={t("createmailboxwizardmodal.redirect_a_copy_of_incoming_mail_to_an_exter")}
        rules={[{ type: "email", message: "Must be a valid email" }]}
      >
        <Input placeholder="external@example.com" autoComplete="off" />
      </Form.Item>
      <Form.Item name="forward_keep_copy" valuePropName="checked" noStyle>
        <Checkbox>Keep a copy in this mailbox when forwarding</Checkbox>
      </Form.Item>
    </>
  ) : (
    pickDomainFirst
  );

  const groupsTab = chosenDomain ? (
    <Form.Item
      label={t("createmailboxwizardmodal.add_to_groups")}
      name="group_ids"
      tooltip={t("createmailboxwizardmodal.the_mailbox_joins_these_groups_and_gains_the")}
    >
      <Select
        mode="multiple"
        allowClear
        disabled={!domainGroups || domainGroups.length === 0}
        placeholder={t("createmailboxwizardmodal.select_groups_optional")}
        optionFilterProp="label"
        options={(domainGroups ?? []).map((g) => ({ value: g.id, label: g.display_name || g.email }))}
      />
    </Form.Item>
  ) : (
    pickDomainFirst
  );

  // Send As can't be granted until the mailbox exists (it needs the new
  // mailbox's id), so on Add this tab points the operator at Edit rather
  // than a control that couldn't persist.
  const sendAsTab = (
    <Typography.Text type="secondary">
      Send As lets one mailbox send as another. It can only be granted after
      this mailbox exists — create it, then open it from the mailbox list and
      use Edit → Send As.
    </Typography.Text>
  );

  return (
    <Drawer
      title={t("createmailboxwizardmodal.create_mailbox")}
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
        <Tabs
          defaultActiveKey="account"
          items={[
            { key: "account", label: "Account", forceRender: true, children: accountTab },
            { key: "forwarding", label: "Forwarding", forceRender: true, children: forwardingTab },
            { key: "groups", label: "Groups", forceRender: true, children: groupsTab },
            { key: "sendas", label: "Send As", children: sendAsTab },
          ]}
        />
      </Form>
    </Drawer>
  );
};

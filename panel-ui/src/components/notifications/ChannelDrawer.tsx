// ChannelDrawer — the neutral create/edit drawer for a notification channel
// (JAB-336, ADR-0083). The admin (server-wide) and tenant (self-service) shells
// render this exact Module and differ only through the ChannelPolicy their
// adapter supplies: resource path, creatable kinds, default kind, form id, the
// name placeholder, the forced-email note and the SMTP port range. Field
// rendering, validation and the write-only masked-secret affordance are
// identical and live here — previously they were duplicated across the two
// drawers with only cosmetic drift.
//
// Secrets are write-only. A GET redacts every secret field (notifsecret.Redact),
// so an edit seeds a blank secret and shows a "leave blank to keep" placeholder;
// submitting blank preserves the stored value server-side
// (notifsecret.PreserveEmptySecrets, wired on BOTH update paths). AC3.
import { useEffect, useMemo } from "react";
import { Alert, Button, Drawer, Form, Grid, Input, InputNumber, Select, Switch } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts

import { StandardDrawerFooter } from "../StandardActionFooter";
import { useCreateMutation, useUpdateMutation } from "../../hooks/useQueries";
import { sendChannelTest } from "./useChannelActions";
import {
  kindFields,
  kindLabels,
  type ChannelFormConfig,
  type ChannelKind,
} from "../../utils/channelKindConfig";
import type { ChannelPolicy, NotificationChannel } from "./channelPolicy";

type FormValues = {
  name: string;
  kind: ChannelKind;
  enabled: boolean;
  config: ChannelFormConfig;
};

export interface ChannelDrawerProps {
  open: boolean;
  onClose: () => void;
  /** Existing row for edit mode; undefined for create. */
  existing?: NotificationChannel;
  policy: ChannelPolicy;
}

// Placeholder shown for a secret field on edit — the write-only affordance: the
// stored secret was redacted by the GET, so a blank field keeps it. AC3.
const MASKED_SECRET_PLACEHOLDER = "•••••• (leave blank to keep)";

export function ChannelDrawer({ open, onClose, existing, policy }: ChannelDrawerProps) {
  const [form] = Form.useForm<FormValues>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);
  const create = useCreateMutation<NotificationChannel, Partial<FormValues>>({
    resource: policy.resourcePath,
  });
  const update = useUpdateMutation<NotificationChannel, Partial<FormValues>>({
    resource: policy.resourcePath,
  });
  const isEdit = Boolean(existing);
  // Edit keeps the row's kind (the Select is disabled); create uses the policy's
  // default. Never pre-select a kind the server would reject.
  const defaultKind: ChannelKind = existing?.kind ?? policy.defaultKind;

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue({
      name: existing?.name ?? "",
      kind: defaultKind,
      enabled: existing?.enabled ?? true,
      config: existing?.config ?? {},
    });
  }, [open, existing, form, defaultKind]);

  const watchedKind = Form.useWatch<ChannelKind | undefined>("kind", form) ?? defaultKind;
  const watchedConfig = Form.useWatch<ChannelFormConfig | undefined>("config", form);
  const fields = useMemo(() => {
    // Tenant email is forced server-side to the caller's own account address, so
    // rendering the admin email form's destination + SMTP fields would only
    // mislead: the server silently overrides them. Show a note instead and
    // submit no email config. (AC5)
    if (policy.forceOwnEmail && watchedKind === "email") return [];
    const all = kindFields[watchedKind] ?? [];
    return all.filter((f) => {
      if (!f.dependsOn) return true;
      // Treat empty/undefined as the first option so dependent rows stay hidden
      // until the parent has been picked.
      return watchedConfig?.[f.dependsOn.name] === f.dependsOn.value;
    });
  }, [policy.forceOwnEmail, watchedKind, watchedConfig]);

  const handleSubmit = async (values: FormValues) => {
    try {
      if (isEdit && existing) {
        await update.mutateAsync({ id: existing.id, input: values });
        feedback.message.success(`Channel "${values.name}" updated`);
      } else {
        await create.mutateAsync(values);
        feedback.message.success(`Channel "${values.name}" created`);
      }
      onClose();
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Save failed");
    }
  };

  const handleSendTest = async () => {
    if (!existing) return;
    await sendChannelTest(policy, existing);
  };

  return (
    <Drawer
      title={isEdit ? `Edit ${existing?.name ?? "channel"}` : "Add channel"}
      open={open}
      onClose={onClose}
      width={isDesktop ? 520 : undefined}
      placement="right"
      destroyOnClose
      footer={
        <StandardDrawerFooter
          formId={policy.formId}
          primaryText={isEdit ? "Save" : "Create"}
          primaryLoading={create.isPending || update.isPending}
          onCancel={onClose}
          extra={isEdit ? <Button onClick={handleSendTest}>Send test</Button> : null}
        />
      }
    >
      <Form<FormValues>
        id={policy.formId}
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        initialValues={{ kind: defaultKind, enabled: true, config: {} }}
      >
        <Form.Item
          name="name"
          label="Name"
          rules={[
            { required: true, message: "Name required" },
            { max: 120, message: "Max 120 chars" },
          ]}
        >
          <Input placeholder={policy.namePlaceholder} />
        </Form.Item>

        <Form.Item name="kind" label="Kind" rules={[{ required: true }]}>
          <Select
            disabled={isEdit}
            options={policy.allowedKinds.map((k) => ({ value: k, label: kindLabels[k] }))}
          />
        </Form.Item>

        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
          <Switch />
        </Form.Item>

        {watchedKind === "webpush" ? (
          <Alert
            type="info"
            showIcon
            message={policy.webpushNote.message}
            description={policy.webpushNote.description}
          />
        ) : null}

        {policy.forceOwnEmail && watchedKind === "email" && policy.emailNote ? (
          <Alert
            type="info"
            showIcon
            message={policy.emailNote.message}
            description={policy.emailNote.description}
          />
        ) : null}

        {fields.map((f) => {
          const rules: { required?: boolean; message: string }[] = [];
          if (f.required) rules.push({ required: true, message: `${f.label} required` });
          const input = (() => {
            if (f.type === "number") {
              // The ntfy priority field is the historical 1–5 caller; the SMTP
              // port input wants the full TCP range. Split on the policy flag +
              // field name rather than overload FieldSpec so bounds stay legible.
              if (policy.smtpPortFullRange && f.name === "smtp_port") {
                return (
                  <InputNumber min={1} max={65535} style={{ width: "100%" }} placeholder={f.placeholder} />
                );
              }
              return <InputNumber min={1} max={5} style={{ width: "100%" }} />;
            }
            if (f.type === "password") {
              return (
                <Input.Password placeholder={isEdit ? MASKED_SECRET_PLACEHOLDER : f.placeholder} />
              );
            }
            if (f.type === "tags") {
              return <Select mode="tags" tokenSeparators={[",", " "]} placeholder="tag1,tag2" />;
            }
            if (f.type === "select") {
              return <Select options={f.options ?? []} placeholder={f.placeholder} />;
            }
            return <Input placeholder={f.placeholder} />;
          })();
          return (
            <Form.Item
              key={String(f.name)}
              name={["config", f.name]}
              label={f.label}
              rules={rules}
              extra={f.help}
            >
              {input}
            </Form.Item>
          );
        })}
      </Form>
    </Drawer>
  );
}

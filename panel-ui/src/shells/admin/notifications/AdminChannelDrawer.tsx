// AdminChannelDrawer — create + edit drawer for notification channels.
// Kind-specific fields are sourced from channelKindConfig so adding a
// new channel type is a single-file diff.
import { useEffect, useMemo } from "react";
import { StandardDrawerFooter } from "../../../components/StandardActionFooter";
import {
  Alert,
  Button,
  Drawer,
  Form,
  Grid,
  Input,
  InputNumber,
  Select,
  Switch,
  message,
} from "antd";

import { apiClient } from "../../../apiClient";
import { useCreateMutation, useUpdateMutation } from "../../../hooks/useQueries";

import {
  CHANNEL_KINDS,
  kindFields,
  kindLabels,
  type ChannelFormConfig,
  type ChannelKind,
} from "./channelKindConfig";

export type NotificationChannel = {
  id: string;
  name: string;
  kind: ChannelKind;
  config: ChannelFormConfig;
  enabled: boolean;
  // user_id is set for tenant-owned channels (JAB-171); null/undefined = a
  // server-wide admin channel.
  user_id?: string | null;
  created_at?: string;
  updated_at?: string;
};

type FormValues = {
  name: string;
  kind: ChannelKind;
  enabled: boolean;
  config: ChannelFormConfig;
};

export interface AdminChannelDrawerProps {
  open: boolean;
  onClose: () => void;
  // existing row for edit mode, undefined for create
  existing?: NotificationChannel;
}

const RESOURCE = "admin/notifications/channels";

export function AdminChannelDrawer({ open, onClose, existing }: AdminChannelDrawerProps) {
  const [form] = Form.useForm<FormValues>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);
  const create = useCreateMutation<NotificationChannel, Partial<FormValues>>({ resource: RESOURCE });
  const update = useUpdateMutation<NotificationChannel, Partial<FormValues>>({ resource: RESOURCE });
  const isEdit = Boolean(existing);

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue({
      name: existing?.name ?? "",
      kind: existing?.kind ?? "slack",
      enabled: existing?.enabled ?? true,
      config: existing?.config ?? {},
    });
  }, [open, existing, form]);

  const watchedKind = Form.useWatch<ChannelKind | undefined>("kind", form) ?? existing?.kind ?? "slack";
  const watchedConfig = Form.useWatch<ChannelFormConfig | undefined>("config", form);
  const fields = useMemo(() => {
    const all = kindFields[watchedKind] ?? [];
    return all.filter((f) => {
      if (!f.dependsOn) return true;
      const current = watchedConfig?.[f.dependsOn.name];
      // Treat empty/undefined as the first option for select fields so
      // dependent rows stay hidden until the parent has been picked.
      return current === f.dependsOn.value;
    });
  }, [watchedKind, watchedConfig]);

  const handleSubmit = async (values: FormValues) => {
    try {
      if (isEdit && existing) {
        await update.mutateAsync({ id: existing.id, input: values });
        message.success(`Channel "${values.name}" updated`);
      } else {
        await create.mutateAsync(values);
        message.success(`Channel "${values.name}" created`);
      }
      onClose();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Save failed";
      message.error(msg);
    }
  };

  const handleSendTest = async () => {
    if (!existing) return;
    try {
      await apiClient.post(`/${RESOURCE}/${existing.id}/test`);
      message.success(`Test envelope fired for "${existing.name}"`);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Test failed";
      message.error(msg);
    }
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
          formId="channel-form"
          primaryText={isEdit ? "Save" : "Create"}
          primaryLoading={create.isPending || update.isPending}
          onCancel={onClose}
          extra={isEdit ? <Button onClick={handleSendTest}>Send test</Button> : null}
        />
      }
    >
      <Form<FormValues>
        id="channel-form"
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        initialValues={{ kind: "slack", enabled: true, config: {} }}
      >
        <Form.Item
          name="name"
          label="Name"
          rules={[
            { required: true, message: "Name required" },
            { max: 120, message: "Max 120 chars" },
          ]}
        >
          <Input placeholder="Ops Slack" />
        </Form.Item>

        <Form.Item name="kind" label="Kind" rules={[{ required: true }]}>
          <Select
            disabled={isEdit}
            options={CHANNEL_KINDS.map((k) => ({ value: k, label: kindLabels[k] }))}
          />
        </Form.Item>

        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
          <Switch />
        </Form.Item>

        {watchedKind === "webpush" ? (
          <Alert
            type="info"
            showIcon
            message="Web Push has no admin-configured fields"
            description="Subscriptions are created per-browser from the user bell. VAPID keys live in server settings."
          />
        ) : null}

        {fields.map((f) => {
          const rules: { required?: boolean; message: string }[] = [];
          if (f.required) rules.push({ required: true, message: `${f.label} required` });
          const input = (() => {
            if (f.type === "number") {
              // The ntfy priority field is the historical 1–5 caller; the
              // SMTP port input wants the full TCP range. We split on the
              // field name rather than overload FieldSpec so each kind's
              // bounds stay legible.
              if (f.name === "smtp_port") {
                return <InputNumber min={1} max={65535} style={{ width: "100%" }} placeholder={f.placeholder} />;
              }
              return <InputNumber min={1} max={5} style={{ width: "100%" }} />;
            }
            if (f.type === "password") return <Input.Password placeholder={f.placeholder} />;
            if (f.type === "tags") {
              return (
                <Select mode="tags" tokenSeparators={[",", " "]} placeholder="tag1,tag2" />
              );
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

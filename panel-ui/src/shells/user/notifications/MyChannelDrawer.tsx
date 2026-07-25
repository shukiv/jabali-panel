// MyChannelDrawer — tenant create/edit drawer for a user's OWN notification
// channels (JAB-171 phase 5). Mirrors AdminChannelDrawer but points at the
// ownership-scoped /me/notifications/channels surface and offers only the
// tenant-creatable kinds. Secrets are write-only: an edit leaves a secret
// field blank to keep the stored (sealed) value — the placeholder says so.
import { useEffect, useMemo } from "react";
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

import { StandardDrawerFooter } from "../../../components/StandardActionFooter";
import { apiClient } from "../../../apiClient";
import { useCreateMutation, useUpdateMutation } from "../../../hooks/useQueries";
import {
  kindFields,
  kindLabels,
  type ChannelFormConfig,
  type ChannelKind,
} from "../../admin/notifications/channelKindConfig";

// TENANT_KINDS — the kinds a tenant may create in the UI. The safe default
// server allowlist; if an admin has widened it, the server still accepts other
// kinds via API, but the common surface is these four.
export const TENANT_KINDS: ChannelKind[] = ["ntfy", "telegram", "discord", "webpush"];

export type MyChannel = {
  id: string;
  name: string;
  kind: ChannelKind;
  config: ChannelFormConfig;
  enabled: boolean;
  user_id?: string;
  created_at?: string;
  updated_at?: string;
};

type FormValues = {
  name: string;
  kind: ChannelKind;
  enabled: boolean;
  config: ChannelFormConfig;
};

export interface MyChannelDrawerProps {
  open: boolean;
  onClose: () => void;
  existing?: MyChannel;
}

const RESOURCE = "me/notifications/channels";

export function MyChannelDrawer({ open, onClose, existing }: MyChannelDrawerProps) {
  const [form] = Form.useForm<FormValues>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);
  const create = useCreateMutation<MyChannel, Partial<FormValues>>({ resource: RESOURCE });
  const update = useUpdateMutation<MyChannel, Partial<FormValues>>({ resource: RESOURCE });
  const isEdit = Boolean(existing);

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue({
      name: existing?.name ?? "",
      kind: existing?.kind ?? "ntfy",
      enabled: existing?.enabled ?? true,
      config: existing?.config ?? {},
    });
  }, [open, existing, form]);

  const watchedKind = Form.useWatch<ChannelKind | undefined>("kind", form) ?? existing?.kind ?? "ntfy";
  const watchedConfig = Form.useWatch<ChannelFormConfig | undefined>("config", form);
  const fields = useMemo(() => {
    const all = kindFields[watchedKind] ?? [];
    return all.filter((f) => {
      if (!f.dependsOn) return true;
      return watchedConfig?.[f.dependsOn.name] === f.dependsOn.value;
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
      message.error(err instanceof Error ? err.message : "Save failed");
    }
  };

  const handleSendTest = async () => {
    if (!existing) return;
    try {
      await apiClient.post(`/${RESOURCE}/${existing.id}/test`);
      message.success(`Test sent to "${existing.name}"`);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Test failed");
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
          formId="my-channel-form"
          primaryText={isEdit ? "Save" : "Create"}
          primaryLoading={create.isPending || update.isPending}
          onCancel={onClose}
          extra={isEdit ? <Button onClick={handleSendTest}>Send test</Button> : null}
        />
      }
    >
      <Form<FormValues>
        id="my-channel-form"
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        initialValues={{ kind: "ntfy", enabled: true, config: {} }}
      >
        <Form.Item
          name="name"
          label="Name"
          rules={[
            { required: true, message: "Name required" },
            { max: 120, message: "Max 120 chars" },
          ]}
        >
          <Input placeholder="My phone" />
        </Form.Item>

        <Form.Item name="kind" label="Kind" rules={[{ required: true }]}>
          <Select
            disabled={isEdit}
            options={TENANT_KINDS.map((k) => ({ value: k, label: kindLabels[k] }))}
          />
        </Form.Item>

        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
          <Switch />
        </Form.Item>

        {watchedKind === "webpush" ? (
          <Alert
            type="info"
            showIcon
            message="Web Push has no fields to configure"
            description="It delivers to this browser's push subscription, created from the notification bell."
          />
        ) : null}

        {fields.map((f) => {
          const rules: { required?: boolean; message: string }[] = [];
          if (f.required) rules.push({ required: true, message: `${f.label} required` });
          const input = (() => {
            if (f.type === "number") {
              return <InputNumber min={1} max={5} style={{ width: "100%" }} />;
            }
            if (f.type === "password") {
              return (
                <Input.Password placeholder={isEdit ? "•••••• (leave blank to keep)" : f.placeholder} />
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
            <Form.Item key={String(f.name)} name={["config", f.name]} label={f.label} rules={rules} extra={f.help}>
              {input}
            </Form.Item>
          );
        })}
      </Form>
    </Drawer>
  );
}

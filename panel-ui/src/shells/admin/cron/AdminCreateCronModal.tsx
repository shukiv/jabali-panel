// AdminCreateCronModal — create a cron job under any tenant.
// Admin-only (gated by the page that mounts it). Mirrors the
// user-side CreateCronModal but adds a target-user picker as the
// first field. UserID flows into the request body; the server
// uses it only when caller.IsAdmin (security gate in panel-api).
import { useTranslation } from "react-i18next";
import { App, Button, Divider, Drawer, Form, Grid, Input, Radio, Select, Space, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient, createCronJob } from "../../../apiClient";

const SCHEDULE_PRESETS = [
  { label: "Every minute", value: "* * * * *" },
  { label: "Hourly", value: "0 * * * *" },
  { label: "Daily at 3 AM", value: "0 3 * * *" },
  { label: "Weekly (Sun 3 AM)", value: "0 3 * * 0" },
  { label: "Monthly (1st 3 AM)", value: "0 3 1 * *" },
  { label: "Advanced", value: "advanced" },
];

interface TargetUser {
  id: string;
  username: string;
  email: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onSuccess: () => void;
}

export const AdminCreateCronModal = ({ open, onClose, onSuccess }: Props) => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const screens = Grid.useBreakpoint();
  const [form] = Form.useForm<{
    run_as: "root" | "tenant";
    user_id: string;
    name: string;
    command: string;
    preset: string;
    schedule: string;
  }>();
  const [saving, setSaving] = useState(false);
  const [preset, setPreset] = useState<string>("0 3 * * *");
  const [runAs, setRunAs] = useState<"root" | "tenant">("tenant");

  // Fetch tenants for the user picker. 500 cap is generous; if a
  // panel has more, the picker's free-text search filters in-place.
  const usersQ = useQuery({
    queryKey: ["admin-cron-target-users"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: TargetUser[] }>(
        "/users?page_size=500",
      );
      return data.data || [];
    },
    enabled: open,
  });

  useEffect(() => {
    if (open) {
      form.resetFields();
      form.setFieldsValue({ run_as: "tenant", preset: "0 3 * * *" });
      setPreset("0 3 * * *");
      setRunAs("tenant");
    }
  }, [open, form]);

  const userOptions = useMemo(
    () =>
      (usersQ.data || []).map((u) => ({
        value: u.id,
        label: `${u.username} <${u.email}>`,
      })),
    [usersQ.data],
  );

  const handleSubmit = async () => {
    let values;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    const schedule = values.preset === "advanced" ? values.schedule : values.preset;
    setSaving(true);
    try {
      await createCronJob({
        name: values.name,
        command: values.command,
        schedule,
        ...(values.run_as === "tenant" ? { user_id: values.user_id } : { run_as_root: true }),
      });
      message.success("Cron job created");
      onSuccess();
    } catch (err) {
      const msg =
        (err as { response?: { data?: { detail?: string } }; message?: string })?.response?.data?.detail ??
        (err as { message?: string })?.message ??
        "Failed to create cron job";
      message.error(msg);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={t("admincreatecronmodal.new_cron_job_as_tenant")}
      width={screens.xs ? "100%" : 560}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="primary" loading={saving} onClick={handleSubmit}>
            Create
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Create a cron job under any tenant's account. The command runs as that
        tenant's Linux user inside their cgroup slice.
      </Typography.Paragraph>
      <Form
        form={form}
        layout="vertical"
        initialValues={{ preset: "0 3 * * *" }}
        onValuesChange={(c) => {
          if (c.preset !== undefined) setPreset(c.preset);
          if (c.run_as !== undefined) setRunAs(c.run_as);
        }}
      >
        <Form.Item label={t("admincreatecronmodal.run_as")} name="run_as" initialValue="tenant">
          <Radio.Group>
            <Radio value="tenant">Tenant (per-user systemd)</Radio>
            <Radio value="root">Root (system-scoped systemd)</Radio>
          </Radio.Group>
        </Form.Item>
        {runAs === "tenant" && (
          <Form.Item
            label={t("admincreatecronmodal.tenant")}
            name="user_id"
            rules={[{ required: true, message: "Pick a tenant" }]}
          >
            <Select
              placeholder={t("admincreatecronmodal.choose_tenant")}
              options={userOptions}
              loading={usersQ.isLoading}
              showSearch
              filterOption={(input, option) =>
                (option?.label?.toString() ?? "").toLowerCase().includes(input.toLowerCase())
              }
            />
          </Form.Item>
        )}
        <Form.Item
          label={t("admincreatecronmodal.name")}
          name="name"
          rules={[
            { required: true, message: "Name is required" },
            { max: 100, message: "Up to 100 chars" },
          ]}
        >
          <Input placeholder="e.g. nightly-backup" />
        </Form.Item>
        <Form.Item
          label={t("admincreatecronmodal.command")}
          name="command"
          rules={[{ required: true, message: "Command is required" }]}
        >
          <Input.TextArea placeholder="cd /home/tenant && ./run.sh" rows={3} />
        </Form.Item>
        <Divider />
        <Form.Item label={t("admincreatecronmodal.schedule")} name="preset">
          <Radio.Group>
            <Space direction="vertical" size="small" style={{ width: "100%" }}>
              {SCHEDULE_PRESETS.map((p) => (
                <Radio key={p.value} value={p.value}>
                  {p.label}
                  {p.value !== "advanced" && (
                    <Typography.Text code style={{ marginLeft: 8 }}>
                      {p.value}
                    </Typography.Text>
                  )}
                </Radio>
              ))}
            </Space>
          </Radio.Group>
        </Form.Item>
        {preset === "advanced" && (
          <Form.Item
            label={t("admincreatecronmodal.custom_cron_expression")}
            name="schedule"
            rules={[{ required: true, message: "Cron expression required" }]}
          >
            <Input placeholder="*/15 * * * *" />
          </Form.Item>
        )}
      </Form>
    </Drawer>
  );
};

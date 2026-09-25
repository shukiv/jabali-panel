// AdminCreateCronModal — create a cron job under any tenant, or edit an
// existing one (GH #1686 item 2). Admin-only (gated by the page that mounts
// it). Mirrors the user-side CreateCronModal but adds a target-user picker as
// the first field. UserID flows into the request body; the server uses it only
// when caller.IsAdmin (security gate in panel-api). In edit mode the owner and
// run-as target are fixed: PATCH /cron/:id accepts only name/command/schedule,
// and the server validates the command against the job owner's directories.
import { useTranslation } from "react-i18next";
import { App, Button, Divider, Drawer, Form, Grid, Input, Radio, Select, Space, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient, createCronJob, updateCronJob } from "../../../apiClient";
import { CRON_SCHEDULE_OPTIONS } from "../../../utils/cronSchedule";
import { CronCommandHelp } from "../../../components/cron/CronCommandHelp";
import { cronErrorHeadline, type CronErrorData } from "../../../components/cron/cronErrorHeadline";
import type { CronWorkspaceRow } from "../../../components/cron/cronColumns";

interface TargetUser {
  id: string;
  username: string;
  email: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onSuccess: () => void;
  initial?: CronWorkspaceRow | null;
}

const DEFAULT_PRESET = "0 3 * * *";

export const AdminCreateCronModal = ({ open, onClose, onSuccess, initial }: Props) => {
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
  const [preset, setPreset] = useState<string>(DEFAULT_PRESET);
  const [runAs, setRunAs] = useState<"root" | "tenant">("tenant");

  const isEditing = !!initial;
  const owner = initial ? initial.username || initial.user_id : "";

  // Fetch tenants for the user picker. 500 cap is generous; if a
  // panel has more, the picker's free-text search filters in-place.
  // Edit mode never shows the picker (the owner is fixed), so skip it.
  const usersQ = useQuery({
    queryKey: ["admin-cron-target-users"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: TargetUser[] }>(
        "/users?page_size=500",
      );
      return data.data || [];
    },
    enabled: open && !isEditing,
  });

  // Sync the whole form every time the drawer opens. The form instance lives
  // in this never-unmounting component, so the Form's initialValues only seed
  // the very first open; every later open must be driven from here or an Edit
  // would show the previous open's values (the GH #1686 item 1 trap on the
  // tenant drawer). A non-preset schedule opens in "advanced" mode with the raw
  // expression, so saving never silently rewrites it to a preset.
  useEffect(() => {
    if (!open) return;
    form.resetFields();
    if (initial) {
      const target = initial.run_as_root ? "root" : "tenant";
      const isPreset = CRON_SCHEDULE_OPTIONS.some(
        (p) => p.value !== "advanced" && p.value === initial.schedule,
      );
      const nextPreset = isPreset ? initial.schedule : "advanced";
      form.setFieldsValue({
        run_as: target,
        name: initial.name,
        command: initial.command,
        preset: nextPreset,
        schedule: isPreset ? undefined : initial.schedule,
      });
      setPreset(nextPreset);
      setRunAs(target);
    } else {
      form.setFieldsValue({ run_as: "tenant", preset: DEFAULT_PRESET });
      setPreset(DEFAULT_PRESET);
      setRunAs("tenant");
    }
  }, [open, initial, form]);

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
      if (initial) {
        // Owner and run-as are immutable on edit — send only the editable
        // fields, never user_id / run_as_root.
        await updateCronJob(initial.id, {
          name: values.name,
          command: values.command,
          schedule,
        });
        message.success("Cron job updated");
      } else {
        await createCronJob({
          name: values.name,
          command: values.command,
          schedule,
          ...(values.run_as === "tenant" ? { user_id: values.user_id } : { run_as_root: true }),
        });
        message.success("Cron job created");
      }
      onSuccess();
    } catch (err) {
      // Route the structured cronops validation error through the shared map so
      // the admin door shows the same friendly, per-field message as the tenant
      // door instead of the raw backend detail (GH #1686 item 5).
      const e = err as { response?: { data?: CronErrorData }; message?: string };
      const fallback = isEditing ? "Failed to update cron job" : "Failed to create cron job";
      const { field, headline } = cronErrorHeadline(e?.response?.data, e?.message ?? fallback);
      if (field) {
        form.setFields([{ name: field, errors: [headline] }]);
      }
      message.error(headline);
    } finally {
      setSaving(false);
    }
  };

  const title = isEditing
    ? runAs === "root"
      ? t("admincreatecronmodal.edit_cron_job_as_root")
      : t("admincreatecronmodal.edit_cron_job_as_tenant")
    : runAs === "root"
      ? t("admincreatecronmodal.new_cron_job_as_root")
      : t("admincreatecronmodal.new_cron_job_as_tenant");

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={title}
      width={screens.xs ? "100%" : 560}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="primary" loading={saving} onClick={handleSubmit}>
            {isEditing ? "Save" : "Create"}
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {isEditing ? (
          runAs === "root" ? (
            <>
              This system cron job runs as <code>root</code> (uid 0) via a
              system-scoped systemd timer, outside any tenant cgroup slice. The
              same command restrictions apply as for tenant crons (see below).
              The run-as target can't be changed here.
            </>
          ) : (
            <>
              This cron job runs as <strong>{owner}</strong>'s Linux user inside
              their cgroup slice, and its command is checked against that
              tenant's own directories. The owner can't be changed here — delete
              the job and create a new one to move it.
            </>
          )
        ) : runAs === "root" ? (
          <>
            Create a system cron job that runs as <code>root</code> (uid 0) via a
            system-scoped systemd timer, outside any tenant cgroup slice. The
            same command restrictions apply as for tenant crons (see below) —
            arbitrary shell commands such as <code>ls</code> are rejected — and
            scripts must live under <code>/root</code> or in one of your own
            account's docroots.
          </>
        ) : (
          <>
            Create a cron job under any tenant's account. The command runs as
            that tenant's Linux user inside their cgroup slice.
          </>
        )}
      </Typography.Paragraph>
      <Form
        form={form}
        layout="vertical"
        initialValues={{ preset: DEFAULT_PRESET }}
        onValuesChange={(c) => {
          if (c.preset !== undefined) setPreset(c.preset);
          if (c.run_as !== undefined) setRunAs(c.run_as);
        }}
      >
        {isEditing ? (
          // Read-only target: owner / run-as are fixed on edit.
          runAs === "root" ? (
            <Form.Item label={t("admincreatecronmodal.run_as")}>
              <Typography.Text strong>root</Typography.Text>
            </Form.Item>
          ) : (
            <Form.Item label={t("admincreatecronmodal.tenant")}>
              <Typography.Text strong>{owner}</Typography.Text>
            </Form.Item>
          )
        ) : (
          <>
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
          </>
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
          extra={<CronCommandHelp />}
        >
          <Input.TextArea
            placeholder={
              runAs === "root"
                ? "php /root/maintenance/cleanup.php"
                : "php /home/tenant/example.com/public_html/cron.php"
            }
            rows={3}
          />
        </Form.Item>
        <Divider />
        <Form.Item label={t("admincreatecronmodal.schedule")} name="preset">
          <Radio.Group>
            <Space direction="vertical" size="small" style={{ width: "100%" }}>
              {CRON_SCHEDULE_OPTIONS.map((p) => (
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

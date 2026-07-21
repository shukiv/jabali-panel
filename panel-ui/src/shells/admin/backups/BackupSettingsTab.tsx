// BackupSettingsTab — knobs that govern the in-process backup
// scheduler/dispatcher. Backed by server_settings (PATCH /admin/settings).
// Retention is per-schedule (Schedules tab) — not server-wide.
import { Button, Form, Input, InputNumber, Spin, message } from "antd";
import { SaveOutlined } from "@icons";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

interface BackupSettingsShape {
  backup_max_concurrent_jobs: number;
  // GH #454: the admin-owned time tenant scheduled backups run at. Tenants pick
  // content + destination; only the admin sets the cron.
  tenant_backup_cron: string;
}

interface ServerSettingsResponse {
  backup_max_concurrent_jobs?: number;
  tenant_backup_cron?: string;
}

export const BackupSettingsTab = () => {
  const [form] = Form.useForm<BackupSettingsShape>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<ServerSettingsResponse>("/admin/settings");
        if (cancelled) return;
        form.setFieldsValue({
          backup_max_concurrent_jobs: resp.data.backup_max_concurrent_jobs ?? 2,
          tenant_backup_cron: resp.data.tenant_backup_cron ?? "0 3 * * *",
        });
      } catch (err) {
        message.error(extractApiError(err, "Load failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [form]);

  const handleSubmit = async (values: BackupSettingsShape) => {
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", values);
      message.success("Settings saved");
    } catch (err) {
      message.error(extractApiError(err, "Save failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Spin spinning={loading}>
      <Form<BackupSettingsShape>
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        style={{ maxWidth: 480 }}
      >
        <Form.Item
          name="backup_max_concurrent_jobs"
          label="Max concurrent backup jobs"
          tooltip="The dispatcher runs at most this many backup_jobs at once. Scheduled jobs queue and start as slots free."
          rules={[{ required: true, type: "number", min: 1, max: 64 }]}
        >
          <InputNumber min={1} max={64} style={{ width: "100%" }} />
        </Form.Item>
        <Form.Item
          name="tenant_backup_cron"
          label="Tenant scheduled-backup time (cron)"
          tooltip="Tenants choose what to back up and where; you own when. 5-field cron (min hour day-of-month month day-of-week), e.g. '0 3 * * *' = daily at 03:00 UTC."
          rules={[{ required: true, message: "A cron expression is required" }]}
        >
          <Input placeholder="0 3 * * *" style={{ fontFamily: "monospace" }} />
        </Form.Item>
        <Form.Item>
          <Button type="primary" htmlType="submit" loading={saving} icon={<SaveOutlined />}>
            Save
          </Button>
        </Form.Item>
      </Form>
    </Spin>
  );
};

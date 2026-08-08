// BackupSettingsTab — knobs that govern the in-process backup
// scheduler/dispatcher. Backed by server_settings (PATCH /admin/settings).
// Retention is per-schedule (Schedules tab) — not server-wide.
import { useTranslation } from "react-i18next";
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
  // GH #454 7B: admin maintenance window (UTC HH:MM) that tenant multi-schedules
  // fire inside. Governs plans with max_backup_schedules > 1; the cron above
  // still governs legacy single-schedule plans.
  tenant_backup_window_start: string;
  tenant_backup_window_end: string;
}

interface ServerSettingsResponse {
  backup_max_concurrent_jobs?: number;
  tenant_backup_cron?: string;
  tenant_backup_window_start?: string;
  tenant_backup_window_end?: string;
}

// HH:MM (24h) — the format the API validates (internalbackup.ValidHHMM).
const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;

export const BackupSettingsTab = () => {
  const { t } = useTranslation();
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
          tenant_backup_window_start: resp.data.tenant_backup_window_start ?? "02:00",
          tenant_backup_window_end: resp.data.tenant_backup_window_end ?? "05:00",
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
          label={t("backupsettingstab.max_concurrent_backup_jobs")}
          tooltip={t("backupsettingstab.the_dispatcher_runs_at_most_this_many_backup")}
          rules={[{ required: true, type: "number", min: 1, max: 64 }]}
        >
          <InputNumber min={1} max={64} style={{ width: "100%" }} />
        </Form.Item>
        <Form.Item
          name="tenant_backup_cron"
          label={t("backupsettingstab.tenant_scheduled_backup_time_cron")}
          tooltip={t("backupsettingstab.tenants_choose_what_to_back_up_and_where_you")}
          rules={[{ required: true, message: "A cron expression is required" }]}
          extra="Runs in UTC (server time) — e.g. 0 3 * * * fires at 03:00 UTC. The tenant view shows the same UTC time."
        >
          <Input placeholder="0 3 * * *" style={{ fontFamily: "monospace" }} />
        </Form.Item>
        <Form.Item
          name="tenant_backup_window_start"
          label={t("backupsettingstab.tenant_backup_window_start")}
          tooltip={t("backupsettingstab.the_maintenance_window_multischedule_tenants")}
          rules={[{ required: true, pattern: HHMM, message: "Use HH:MM (UTC)" }]}
          extra="UTC. Applies to plans that allow more than one schedule; the scheduler fires (and spreads) tenant backups within this window."
        >
          <Input placeholder="02:00" style={{ fontFamily: "monospace" }} />
        </Form.Item>
        <Form.Item
          name="tenant_backup_window_end"
          label={t("backupsettingstab.tenant_backup_window_end")}
          rules={[{ required: true, pattern: HHMM, message: "Use HH:MM (UTC)" }]}
        >
          <Input placeholder="05:00" style={{ fontFamily: "monospace" }} />
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

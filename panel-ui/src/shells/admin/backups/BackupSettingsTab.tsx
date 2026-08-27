// BackupSettingsTab — knobs that govern the in-process backup
// scheduler/dispatcher. Backed by server_settings (PATCH /admin/settings).
// Retention is per-schedule (Schedules tab) — not server-wide.
import { useTranslation } from "react-i18next";
import { Button, Form, Input, InputNumber, Spin, Switch } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
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
  // GH #1097: the window is an OPT-IN restriction, off by default. When off the
  // tenant scheduler ignores it and runs each schedule on its chosen interval.
  tenant_backup_window_enforce: boolean;
  // GH #1240: opt-in automatic daily local backups for every user.
  default_local_backups_enabled: boolean;
}

interface ServerSettingsResponse {
  backup_max_concurrent_jobs?: number;
  tenant_backup_cron?: string;
  tenant_backup_window_start?: string;
  tenant_backup_window_end?: string;
  tenant_backup_window_enforce?: boolean;
  default_local_backups_enabled?: boolean;
}

// HH:MM (24h) — the format the API validates (internalbackup.ValidHHMM).
const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;

export const BackupSettingsTab = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm<BackupSettingsShape>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  // GH #1097: the window inputs only apply when enforcement is on.
  const enforceWindow = Form.useWatch("tenant_backup_window_enforce", form);

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
          tenant_backup_window_enforce: resp.data.tenant_backup_window_enforce ?? false,
          default_local_backups_enabled: resp.data.default_local_backups_enabled ?? false,
        });
      } catch (err) {
        feedback.message.error(extractApiError(err, "Load failed"));
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
      feedback.message.success("Settings saved");
    } catch (err) {
      feedback.message.error(extractApiError(err, "Save failed"));
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
          name="default_local_backups_enabled"
          valuePropName="checked"
          label="Automatic daily local backups for all users"
          tooltip="Off by default. When on, the panel keeps a managed schedule that backs up every user (files + databases + mail) to the local repo daily at 05:00 UTC, keeping 3 days. New users are covered automatically. Uses local disk + IO — prefer a remote destination if disk is tight. Turning it off disables the managed schedule but never removes users' own schedules."
        >
          <Switch />
        </Form.Item>
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
          name="tenant_backup_window_enforce"
          valuePropName="checked"
          label="Restrict tenant backups to a maintenance window"
          tooltip="Off by default. When off, a tenant's Hourly / 6-hourly / 12-hourly / Daily schedule runs on its own interval. Turn on to confine all tenant scheduled backups to the UTC window below (e.g. to bound server load)."
        >
          <Switch />
        </Form.Item>
        <Form.Item
          name="tenant_backup_window_start"
          label={t("backupsettingstab.tenant_backup_window_start")}
          tooltip={t("backupsettingstab.the_maintenance_window_multischedule_tenants")}
          rules={[{ required: true, pattern: HHMM, message: "Use HH:MM (UTC)" }]}
          extra="UTC. Only used when the restriction above is on — the scheduler fires (and spreads) tenant backups within this window."
        >
          <Input placeholder="02:00" style={{ fontFamily: "monospace" }} disabled={!enforceWindow} />
        </Form.Item>
        <Form.Item
          name="tenant_backup_window_end"
          label={t("backupsettingstab.tenant_backup_window_end")}
          rules={[{ required: true, pattern: HHMM, message: "Use HH:MM (UTC)" }]}
        >
          <Input placeholder="05:00" style={{ fontFamily: "monospace" }} disabled={!enforceWindow} />
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

import { useEffect, useState } from "react";
import { Alert, Card, Select, Space, Switch, Typography, notification } from "antd";

import { apiClient } from "../../../apiClient";
import { kindLabels, type ChannelKind } from "../notifications/channelKindConfig";

// TenantNotificationsCard — Server Settings → General: the master switch for the
// tenant-facing /me/notifications surface (JAB-171) plus the admin-controlled
// allowlist of channel KINDS a tenant may create. Off by default; the surface
// only comes to life once an admin flips it. The allowlist gates creation AND
// live delivery server-side, so narrowing it stops existing tenant channels of a
// removed kind.

// Kinds an admin may allow tenants to configure. Mirrors the server's
// TenantConfigurableKinds; safe kinds first, higher-risk ones flagged in help.
const CONFIGURABLE_KINDS: ChannelKind[] = [
  "ntfy",
  "telegram",
  "discord",
  "webpush",
  "webhook",
  "slack",
  "sms",
  "email",
];

export const TenantNotificationsCard = () => {
  const [enabled, setEnabled] = useState(false);
  const [kinds, setKinds] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [savingToggle, setSavingToggle] = useState(false);
  const [savingKinds, setSavingKinds] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{
          tenant_notifications_enabled?: boolean;
          tenant_notification_kinds?: string[];
        }>("/admin/settings");
        if (!cancelled) {
          setEnabled(resp.data.tenant_notifications_enabled === true);
          setKinds(resp.data.tenant_notification_kinds ?? []);
        }
      } catch {
        if (!cancelled) notification.error({ message: "Failed to load notification settings" });
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const onToggle = async (next: boolean) => {
    setSavingToggle(true);
    try {
      await apiClient.patch("/admin/settings", { tenant_notifications_enabled: next });
      setEnabled(next);
      notification.success({ message: next ? "Tenant notifications enabled" : "Tenant notifications disabled" });
    } catch {
      notification.error({ message: "Failed to update setting" });
    } finally {
      setSavingToggle(false);
    }
  };

  const onKindsChange = async (next: string[]) => {
    setSavingKinds(true);
    try {
      const resp = await apiClient.patch<{ tenant_notification_kinds?: string[] }>("/admin/settings", {
        tenant_notification_kinds: next,
      });
      // The server sanitizes (drops unknown kinds) and returns the effective set.
      setKinds(resp.data.tenant_notification_kinds ?? next);
      notification.success({ message: "Allowed channel kinds updated" });
    } catch {
      notification.error({ message: "Failed to update allowed kinds" });
    } finally {
      setSavingKinds(false);
    }
  };

  return (
    <Card title="Tenant notifications" style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Let non-admin users configure their own notification channels (ntfy, Telegram,
        Discord, web push, and more) and route their own events to them. Off by default —
        the whole surface is inert until you turn it on. Turning it off is a live kill
        switch: tenant channels stop receiving anything.
      </Typography.Paragraph>
      <Switch
        checked={enabled}
        loading={savingToggle}
        onChange={onToggle}
        checkedChildren="On"
        unCheckedChildren="Off"
      />

      <Typography.Paragraph type="secondary" style={{ marginTop: 20, marginBottom: 8 }}>
        <strong>Allowed channel kinds.</strong> Which kinds a tenant may create. Empty means
        the safe default set (ntfy, Telegram, Discord, web push). Narrowing the list also
        stops delivery to existing tenant channels of a removed kind.
      </Typography.Paragraph>
      <Space direction="vertical" style={{ width: "100%" }}>
        <Select
          mode="multiple"
          style={{ width: "100%", maxWidth: 520 }}
          value={kinds}
          loading={savingKinds}
          onChange={onKindsChange}
          placeholder="Safe defaults (ntfy, Telegram, Discord, web push)"
          options={CONFIGURABLE_KINDS.map((k) => ({ value: k, label: kindLabels[k] }))}
        />
        <Alert
          type="warning"
          showIcon
          message="Higher-risk kinds"
          description="webhook / Slack / SMS send to tenant-supplied URLs (SSRF-guarded, but still tenant-controlled). email is forced to the tenant's own account address over local delivery. Only allow these if you trust your tenants."
        />
      </Space>
    </Card>
  );
};

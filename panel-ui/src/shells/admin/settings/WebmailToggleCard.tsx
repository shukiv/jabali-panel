import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Card, Switch, Typography, notification } from "antd";

import { apiClient } from "../../../apiClient";

// WebmailToggleCard — Server Settings → Email: enable/disable the bundled
// Bulwark webmail client server-wide (GH #316). When off, the reconciler tears
// down the webmail vhosts and stops the jabali-webmail service.
export const WebmailToggleCard = () => {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(true);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ webmail_enabled?: boolean }>("/admin/settings");
        if (!cancelled) setEnabled(resp.data.webmail_enabled !== false);
      } catch {
        if (!cancelled) notification.error({ message: "Failed to load webmail setting" });
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const onToggle = async (next: boolean) => {
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", { webmail_enabled: next });
      setEnabled(next);
      notification.success({
        message: next ? "Webmail enabled" : "Webmail disabled",
        description: next
          ? "The webmail vhosts will be (re)served on the next reconcile."
          : "Webmail vhosts are being torn down and the webmail service stopped.",
      });
    } catch {
      notification.error({ message: "Failed to update webmail setting" });
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title={t("webmailtogglecard.webmail")} style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Serve the bundled webmail client (Bulwark) at <Typography.Text code>mail.&lt;domain&gt;</Typography.Text>{" "}
        for every email-enabled domain. Turn it off to stop offering webmail
        server-wide — mail delivery (IMAP/SMTP/JMAP) is unaffected.
      </Typography.Paragraph>
      <Switch
        checked={enabled}
        loading={saving}
        onChange={onToggle}
        checkedChildren="On"
        unCheckedChildren="Off"
      />
    </Card>
  );
};

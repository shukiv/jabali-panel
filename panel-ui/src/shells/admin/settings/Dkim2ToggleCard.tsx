import { useEffect, useState } from "react";
import { Card, Switch, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../../../apiClient";

// Dkim2ToggleCard — Server Settings → Email: opt-in DKIM2 chain-of-custody
// signing (GH #648, draft-ietf-dkim-dkim2). Needs no DNS change: DKIM2
// verifies against the DKIM records the panel already publishes, and the
// classic DKIM signature keeps signing alongside for legacy verifiers.
export const Dkim2ToggleCard = () => {
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ dkim2_signing_enabled?: boolean }>("/admin/settings");
        if (!cancelled) setEnabled(resp.data.dkim2_signing_enabled === true);
      } catch {
        if (!cancelled) feedback.notification.error({ message: "Failed to load DKIM2 setting" });
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
      await apiClient.patch("/admin/settings", { dkim2_signing_enabled: next });
      setEnabled(next);
      feedback.notification.success({
        message: next ? "DKIM2 signing enabled" : "DKIM2 signing disabled",
        description: next
          ? "Every mail domain gets a DKIM2 signature on the next reconcile — no DNS changes needed."
          : "DKIM2 signatures are being removed; classic DKIM keeps signing.",
      });
    } catch {
      feedback.notification.error({ message: "Failed to update DKIM2 setting" });
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card
      title={
        <>
          DKIM2 signing <Tag color="orange">experimental</Tag>
        </>
      }
      style={{ marginBottom: 16 }}
      loading={loading}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Add a DKIM2 chain-of-custody signature to outgoing mail on every
        email-enabled domain, alongside the classic DKIM signature. Uses the
        same keys and DNS records already published — nothing to change at
        your registrar. DKIM2 is an IETF working-group draft; receivers that
        don&apos;t support it ignore the extra header, and classic DKIM keeps
        signing either way.
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

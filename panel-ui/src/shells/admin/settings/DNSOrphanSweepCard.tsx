import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Card, Switch, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../../../apiClient";

// DNSOrphanSweepCard — Server Settings → DNS: opt in to the automatic PowerDNS
// orphan sweep (GH #1620). When on (and DNS is enabled), the reconciler
// periodically fires `dns.reap-orphans` to delete PowerDNS zones/records that
// no longer map to any panel domain — the same cleanup the operator CLI
// (`jabali dns prune-orphan-records`) does on demand. Off by default; the
// reconciler is a no-op while this or the DNS feature is off.
export const DNSOrphanSweepCard = () => {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ dns_orphan_autosweep_enabled?: boolean }>("/admin/settings");
        if (!cancelled) {
          setEnabled(resp.data.dns_orphan_autosweep_enabled === true);
        }
      } catch {
        if (!cancelled) feedback.message.error("Failed to load DNS orphan-sweep setting");
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
      await apiClient.patch("/admin/settings", { dns_orphan_autosweep_enabled: next });
      setEnabled(next);
      feedback.message.success(next ? "DNS orphan sweep enabled" : "DNS orphan sweep disabled");
    } catch {
      feedback.message.error("Failed to update setting");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title={t("dnsorphansweepcard.dns_orphan_sweep")} style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Automatically delete orphaned PowerDNS zones and records — ones that no
        longer belong to any panel domain (for example, left behind by a failed
        delete) — on the reconciler&apos;s regular schedule. This is the same
        cleanup the <code>jabali dns prune-orphan-records</code> command runs on
        demand. Off by default. Has no effect unless the DNS feature is also
        enabled.
      </Typography.Paragraph>
      <Switch checked={enabled} loading={saving} onChange={onToggle} checkedChildren="On" unCheckedChildren="Off" />
    </Card>
  );
};

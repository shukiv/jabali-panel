import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Card, Switch, Typography, notification } from "antd";

import { apiClient } from "../../../apiClient";

// TenantDomainOptionsCard — Server Settings → General: opt non-admin domain
// owners into the curated safe nginx options (GH #307). Off by default; raw
// nginx directives stay admin-only regardless.
export const TenantDomainOptionsCard = () => {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ tenant_domain_options_enabled?: boolean }>("/admin/settings");
        if (!cancelled) setEnabled(resp.data.tenant_domain_options_enabled === true);
      } catch {
        if (!cancelled) notification.error({ message: "Failed to load tenant domain options setting" });
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
      await apiClient.patch("/admin/settings", { tenant_domain_options_enabled: next });
      setEnabled(next);
      notification.success({ message: next ? "Tenant domain options enabled" : "Tenant domain options disabled" });
    } catch {
      notification.error({ message: "Failed to update setting" });
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title={t("tenantdomainoptionscard.tenant_domain_options")} style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Let non-admin users set a curated, safe set of nginx options on their own
        domains — max upload size, HSTS, security headers, and gzip. The panel
        renders fixed directives; raw nginx directives and the rule builder stay
        admin-only either way.
      </Typography.Paragraph>
      <Switch checked={enabled} loading={saving} onChange={onToggle} checkedChildren="On" unCheckedChildren="Off" />
    </Card>
  );
};

// SettingsTab — per-domain mail settings for the tenant. GH #1628 slice 4:
// surfaces the per-domain webmail (Bulwark) toggle so a tenant can drop the
// mail.<domain> webmail vhost for a domain they own, without an admin.
//
// Webmail is one of three ANDed gates (server-wide, this per-domain flag, and
// the hosting-package entitlement). Turning it ON here only takes effect when
// the plan and server also allow it; turning it OFF always drops the vhost.
// The write is the owner-scoped PATCH /domains/:id (webmail_enabled) that the
// admin domain-email section and the create drawer already use — no new
// capability, and convergence is tick-reliant like the admin toggle.
import { useState } from "react";
import { Alert, Card, Skeleton, Space, Switch, Typography } from "antd";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts
import { useOneQuery, useUpdateMutation } from "../../../../hooks/useQueries";
import type { Domain } from "../../../../components/domains/types";

export const SettingsTab = ({ domainId }: { domainId?: string } = {}) => {
  const { data: domain, isLoading } = useOneQuery<Domain>({
    resource: "domains",
    id: domainId,
  });
  const update = useUpdateMutation<Domain>({ resource: "domains" });
  const [flipping, setFlipping] = useState(false);

  const onWebmailFlip = async (next: boolean) => {
    if (!domainId) return;
    setFlipping(true);
    try {
      await update.mutateAsync({ id: domainId, input: { webmail_enabled: next } });
      feedback.message.success(
        next ? "Webmail enabled for this domain" : "Webmail disabled for this domain",
      );
    } catch {
      feedback.message.error("Failed to toggle webmail");
    } finally {
      setFlipping(false);
    }
  };

  if (isLoading && !domain) {
    return <Skeleton active paragraph={{ rows: 2 }} />;
  }
  if (!domain) {
    return <Alert type="error" showIcon message="Failed to load domain settings" />;
  }

  const emailEnabled = domain.email_enabled ?? false;

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Card size="small" title="Webmail">
        {emailEnabled ? (
          <Space direction="vertical" size="middle" style={{ width: "100%" }}>
            <Space size="middle" align="center" wrap>
              <Switch
                checked={domain.webmail_enabled ?? true}
                loading={flipping}
                onChange={onWebmailFlip}
                aria-label="Webmail client"
              />
              <span>
                Webmail client (Bulwark) for{" "}
                <Typography.Text code>{domain.name}</Typography.Text> — turn off to
                drop just the{" "}
                <Typography.Text code>mail.{domain.name}</Typography.Text> webmail
                vhost; mail delivery is unaffected. The change applies on the next
                reconcile.
              </span>
            </Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Webmail also has to be enabled in your hosting plan; turning it on here
              has no effect while your plan has webmail off.
            </Typography.Text>
          </Space>
        ) : (
          <Alert
            type="info"
            showIcon
            message="Enable email for this domain first"
            description="Webmail is only relevant once this domain has email enabled."
          />
        )}
      </Card>
    </Space>
  );
};

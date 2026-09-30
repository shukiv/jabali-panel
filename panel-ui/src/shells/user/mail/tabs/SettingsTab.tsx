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
//
// GH #1915 (johnnyq): the per-domain outbound disclaimer moved here from its
// own tab. It is the same owner-scoped GET/PUT /domains/:id/disclaimer the tab
// used; only the placement changed.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Alert, Button, Card, Form, Input, Skeleton, Space, Switch, Typography } from "antd";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts
import { useOneQuery, useUpdateMutation } from "../../../../hooks/useQueries";
import { useDisclaimer, useUpdateDisclaimer, type Disclaimer } from "../../../../hooks/useDisclaimer";
import type { Domain } from "../../../../components/domains/types";

interface DisclaimerValues {
  enabled: boolean;
  text: string;
}

// DisclaimerForm mounts only once the saved disclaimer has loaded, so the
// form's initialValues are the saved ones (a form rendered before the fetch
// would keep the empty defaults). The parent keys it by updated_at, so a save
// remounts it with what the server stored.
const DisclaimerForm = ({ domainId, saved }: { domainId: string; saved: Disclaimer }) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<DisclaimerValues>();
  const update = useUpdateDisclaimer();

  const onFinish = async (vals: DisclaimerValues) => {
    try {
      await update.mutateAsync({ domainID: domainId, enabled: vals.enabled, text: vals.text ?? "" });
      feedback.message.success("Disclaimer saved");
    } catch (err) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        ?? "Failed to save disclaimer";
      feedback.message.error(msg);
    }
  };

  return (
    <Form<DisclaimerValues>
      form={form}
      layout="vertical"
      initialValues={{ enabled: saved.enabled, text: saved.text }}
      onFinish={onFinish}
    >
      <Form.Item
        name="enabled"
        label={t("disclaimertab.enable_disclaimer")}
        valuePropName="checked"
        extra="Append a disclaimer to all outbound emails from this domain"
      >
        <Switch />
      </Form.Item>
      <Form.Item
        name="text"
        label={t("disclaimertab.disclaimer_text")}
        dependencies={["enabled"]}
        rules={[
          ({ getFieldValue }) => ({
            validator(_, value) {
              if (getFieldValue("enabled") && !value?.trim()) {
                return Promise.reject(new Error("Text required when enabled"));
              }
              return Promise.resolve();
            },
          }),
        ]}
        extra="Plain text. In HTML mail it is shown below a line, and any markup is shown as text."
      >
        <Input.TextArea rows={5} placeholder={t("disclaimertab.if_you_received_this_email_by_mistake_please")} />
      </Form.Item>
      <Button type="primary" htmlType="submit" loading={update.isPending}>
        {t("disclaimertab.save")}
      </Button>
    </Form>
  );
};

const DisclaimerCard = ({ domainId }: { domainId: string }) => {
  const q = useDisclaimer(domainId);
  return (
    <Card size="small" title="Disclaimer">
      {q.isLoading ? (
        <Skeleton active paragraph={{ rows: 3 }} />
      ) : q.data ? (
        <DisclaimerForm key={q.data.updated_at} domainId={domainId} saved={q.data} />
      ) : (
        <Alert type="error" showIcon message="Failed to load the disclaimer" />
      )}
    </Card>
  );
};

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

  if (!(domain.email_enabled ?? false)) {
    return (
      <Alert
        type="info"
        showIcon
        message="Enable email for this domain first"
        description="Webmail and the disclaimer are only relevant once this domain has email enabled."
      />
    );
  }

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Card size="small" title="Webmail">
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
      </Card>
      <DisclaimerCard domainId={domain.id} />
    </Space>
  );
};

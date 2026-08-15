// EmailCard — Settings → Email tab body. Read-only card showing the
// panel-primary mail domain (ADR-0048). Two states:
//   - "ready": domain exists, DKIM may or may not be published
//   - "initializing": row absent yet (fresh-install convergence window)
//
// No edit affordance; the hostname comes from JABALI_SRV_HOSTNAME at
// install time and isn't editable from the panel UI.

import { useTranslation } from "react-i18next";
import { MailOutlined, ReloadOutlined } from "@icons";
import { Alert, Badge, Button, Card, Descriptions, Skeleton, Space, Typography } from "antd";

import { useSettingsEmail } from "../../../hooks/useSettingsEmail";

export const EmailCard = () => {
  const { t } = useTranslation();
  const q = useSettingsEmail();

  if (q.isPending) {
    return (
      <Card title={<CardTitle />} style={{ marginBottom: 16 }}>
        <Skeleton active />
      </Card>
    );
  }

  if (q.isError) {
    return (
      <Card title={<CardTitle />} style={{ marginBottom: 16 }}>
        <Alert
          type="error"
          message={t("emailcard.failed_to_load_email_settings")}
          description={q.error.message}
          action={
            <Button icon={<ReloadOutlined />} onClick={() => q.refetch()}>
              Retry
            </Button>
          }
        />
      </Card>
    );
  }

  const data = q.data;
  if (data.state === "initializing") {
    return (
      <Card
        title={<CardTitle />}
        extra={
          <Button icon={<ReloadOutlined />} onClick={() => q.refetch()}>
            Refresh
          </Button>
        }
      >
        <Alert
          type="info"
          showIcon
          message={t("emailcard.webmail_is_initializing")}
          description={t("emailcard.the_panel_hostname_s_mail_domain_is_being_pr")}
        />
      </Card>
    );
  }

  // state === "ready"
  const enabledAtLabel = data.emailEnabledAt
    ? new Date(data.emailEnabledAt).toLocaleString()
    : "—";

  return (
    <Card title={<CardTitle />}>
      <Descriptions column={1} size="middle" layout="vertical">
        <Descriptions.Item label={t("emailcard.primary_mail_domain")}>
          <Typography.Text code>{data.primaryDomainName}</Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label={t("emailcard.webmail_url")}>
          <Typography.Link href={data.webmailURL} target="_blank" rel="noreferrer">
            {data.webmailURL}
          </Typography.Link>
        </Descriptions.Item>
        <Descriptions.Item label={t("emailcard.dkim")}>
          {data.dkimPublished ? (
            <Badge status="success" text="Published" />
          ) : (
            <Badge status="processing" text="Initializing" />
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t("emailcard.enabled_at")}>{enabledAtLabel}</Descriptions.Item>
      </Descriptions>
      <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
        Auto-registered at install time. The hostname comes from{" "}
        <Typography.Text code>JABALI_SRV_HOSTNAME</Typography.Text> and isn't editable from
        the panel UI. See ADR-0048 for the design decision.
      </Typography.Paragraph>
    </Card>
  );
};

const CardTitle = () => (
  <Space>
    <MailOutlined />
    <span>Email</span>
  </Space>
);

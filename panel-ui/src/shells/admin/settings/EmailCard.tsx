// EmailCard — Settings → Email tab body. Shows the panel-primary mail
// domain (ADR-0048). Two states:
//   - "ready": domain exists, DKIM may or may not be published
//   - "initializing": row absent yet (fresh-install convergence window)
//
// The primary mail domain is the panel hostname's domain and isn't edited
// here. The shared mail hostname (JAB-390) is: the admin requests a change,
// and the reconciler applies it once the name points at this server and its
// certificate is issued. The card shows the request's progress.

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { MailOutlined, ReloadOutlined } from "@icons";
import { Alert, Badge, Button, Card, Descriptions, Input, Skeleton, Space, Tag, Typography } from "antd";

import { extractApiError } from "../../../apiErrors";
import { feedback } from "../../../lib/feedback";
import {
  useCancelMailHostname,
  useRequestMailHostname,
  useSettingsEmail,
  type MailHostnameSwitchover,
  type SettingsEmailReady,
} from "../../../hooks/useSettingsEmail";

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
        <Descriptions.Item label={t("emailcard.mail_hostname")}>
          <Space>
            <Typography.Text code>{data.mailHostname.effective}</Typography.Text>
            {data.mailHostname.applied ? (
              <Tag color="blue">{t("emailcard.mail_hostname_custom")}</Tag>
            ) : (
              <Tag>{t("emailcard.mail_hostname_default")}</Tag>
            )}
          </Space>
        </Descriptions.Item>
      </Descriptions>
      <MailHostnameChange data={data} />
      <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
        {t("emailcard.auto_registered")}
      </Typography.Paragraph>
    </Card>
  );
};

// MailHostnameChange requests, follows and cancels a change of the shared
// panel mail hostname (JAB-390).
const MailHostnameChange = ({ data }: { data: SettingsEmailReady }) => {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const request = useRequestMailHostname();
  const cancel = useCancelMailHostname();
  const derived = `mail.${data.primaryDomainName}`;
  const sw = data.switchover;
  const issuing = sw?.status === "issuing";
  const busy = request.isPending || cancel.isPending;

  const submit = (value: string) => {
    request.mutate(value.trim(), {
      onSuccess: () => {
        setName("");
        feedback.message.success(t("emailcard.change_requested"));
      },
      onError: (e) => feedback.message.error(extractApiError(e)),
    });
  };
  const withdraw = () =>
    cancel.mutate(undefined, {
      onSuccess: () => feedback.message.success(t("emailcard.change_cancelled")),
      onError: (e) => feedback.message.error(extractApiError(e)),
    });

  return (
    <div style={{ marginTop: 16 }}>
      <Typography.Title level={5}>{t("emailcard.change_mail_hostname")}</Typography.Title>
      {sw && sw.status !== "done" && (
        <SwitchoverStatus sw={sw} onCancel={withdraw} cancelling={cancel.isPending} />
      )}
      <Space wrap style={{ marginTop: 8 }}>
        <Space.Compact style={{ width: 420, maxWidth: "100%" }}>
          <Input
            aria-label={t("emailcard.mail_hostname")}
            placeholder={t("emailcard.mail_hostname_placeholder")}
            value={name}
            maxLength={253}
            disabled={issuing}
            onChange={(e) => setName(e.target.value)}
            onPressEnter={() => name.trim() && submit(name)}
          />
          <Button
            type="primary"
            loading={request.isPending}
            disabled={issuing || busy || !name.trim()}
            onClick={() => submit(name)}
          >
            {t("emailcard.request_change")}
          </Button>
        </Space.Compact>
        {data.mailHostname.applied && (
          <Button disabled={issuing || busy} onClick={() => submit(derived)}>
            {t("emailcard.switch_back_to", { name: derived })}
          </Button>
        )}
      </Space>
      <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
        {t("emailcard.mail_hostname_help", { derived, domain: data.primaryDomainName })}
      </Typography.Paragraph>
    </div>
  );
};

const SwitchoverStatus = ({
  sw,
  onCancel,
  cancelling,
}: {
  sw: MailHostnameSwitchover;
  onCancel: () => void;
  cancelling: boolean;
}) => {
  const { t } = useTranslation();
  const cancelButton =
    sw.status === "pending" || sw.status === "failed" ? (
      <Button size="small" loading={cancelling} onClick={onCancel}>
        {t("emailcard.cancel_change")}
      </Button>
    ) : undefined;
  if (sw.status === "failed") {
    return (
      <Alert
        type="warning"
        showIcon
        message={t("emailcard.switchover_failed", { name: sw.desired })}
        description={
          <>
            <div>{sw.lastError}</div>
            {sw.nextRetryAt && (
              <div>{t("emailcard.switchover_retry_at", { time: new Date(sw.nextRetryAt).toLocaleString() })}</div>
            )}
          </>
        }
        action={cancelButton}
      />
    );
  }
  return (
    <Alert
      type="info"
      showIcon
      message={
        sw.status === "issuing"
          ? t("emailcard.switchover_issuing", { name: sw.desired })
          : t("emailcard.switchover_pending", { name: sw.desired })
      }
      action={cancelButton}
    />
  );
};

const CardTitle = () => (
  <Space>
    <MailOutlined />
    <span>Email</span>
  </Space>
);

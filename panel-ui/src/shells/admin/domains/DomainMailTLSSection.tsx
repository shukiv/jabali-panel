// DomainMailTLSSection — M6.6 per-domain mail TLS.
//
// Renders the status chip + DNS A-record preview table + opt-in
// toggle + re-issue button for one domain. Styling mirrors
// DomainSSLSection so the two cert flows feel like siblings in the
// domain detail page.
//
// Wire path:
//   GET    /api/v1/domains/:id/mail-certificate
//   POST   /api/v1/domains/:id/mail-certificate/enable
//   POST   /api/v1/domains/:id/mail-certificate/disable
//   POST   /api/v1/domains/:id/mail-certificate/reissue
import { useTranslation } from "react-i18next";
import { useCallback, useEffect, useState } from "react";
import { Alert, Button, Skeleton, Space, Switch, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { ReloadOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

type MailCertStatus =
  | "not_opted_in"
  | "pending"
  | "dns_missing"
  | "issuing"
  | "issued"
  | "failed"
  | "disabled";

interface MailCertResponse {
  status: MailCertStatus;
  lineage_path?: string;
  last_error?: string | null;
  attempt_count: number;
  issued_at?: string;
  expires_at?: string;
  next_retry_at?: string;
  sans: string[];
}

const STATUS_COLORS: Record<MailCertStatus, string> = {
  not_opted_in: "default",
  pending: "default",
  dns_missing: "gold",
  issuing: "processing",
  issued: "green",
  failed: "red",
  disabled: "default",
};

const STATUS_LABELS: Record<MailCertStatus, string> = {
  not_opted_in: "Not opted in",
  pending: "Pending",
  dns_missing: "DNS missing",
  issuing: "Issuing",
  issued: "Issued",
  failed: "Failed",
  disabled: "Disabled",
};

interface Props {
  domainId: string;
}

export const DomainMailTLSSection = ({ domainId }: Props) => {
  const { t } = useTranslation();
  const [cert, setCert] = useState<MailCertResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);

  const fetchStatus = useCallback(async () => {
    setLoading(true);
    try {
      const res = await apiClient.get<MailCertResponse>(`/domains/${domainId}/mail-certificate`);
      setCert(res.data);
    } catch {
      feedback.message.error("Failed to load mail TLS status");
    } finally {
      setLoading(false);
    }
  }, [domainId]);

  useEffect(() => {
    void fetchStatus();
  }, [fetchStatus]);

  const handleToggle = async (next: boolean) => {
    setBusy(true);
    try {
      if (next) {
        await apiClient.post(`/domains/${domainId}/mail-certificate/enable`);
        feedback.message.success("Mail TLS enabled — reconciler will request a Let's Encrypt cert shortly");
      } else {
        await apiClient.post(`/domains/${domainId}/mail-certificate/disable`);
        feedback.message.success("Mail TLS disabled");
      }
      await fetchStatus();
    } catch (err: unknown) {
      const detail = (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
      feedback.message.error(detail ?? "Toggle failed");
    } finally {
      setBusy(false);
    }
  };

  const handleReissue = async () => {
    setBusy(true);
    try {
      await apiClient.post(`/domains/${domainId}/mail-certificate/reissue`);
      feedback.message.success("Re-issue queued — reconciler will retry on the next tick");
      await fetchStatus();
    } catch {
      feedback.message.error("Re-issue failed");
    } finally {
      setBusy(false);
    }
  };

  if (loading) {
    return <Skeleton active />;
  }
  if (!cert) {
    return null;
  }

  const optedIn = cert.status !== "not_opted_in" && cert.status !== "disabled";
  const expires = cert.expires_at ? new Date(cert.expires_at) : null;
  const daysUntilExpiry = expires ? Math.round((expires.getTime() - Date.now()) / 86_400_000) : null;

  return (
    <div>
      <Space style={{ marginBottom: 12 }} align="center">
        <Switch
          checked={optedIn}
          loading={busy}
          onChange={handleToggle}
        />
        <Typography.Text strong>Per-domain mail TLS</Typography.Text>
        <Tag color={STATUS_COLORS[cert.status]}>{STATUS_LABELS[cert.status]}</Tag>
        {cert.status === "issued" && daysUntilExpiry !== null && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            expires in {daysUntilExpiry}d
          </Typography.Text>
        )}
      </Space>

      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        Issues a Let's Encrypt certificate so mail clients can connect to{" "}
        <code>mail.&lt;your-domain&gt;</code> with a trusted certificate. Only{" "}
        <code>mail.&lt;your-domain&gt;</code> must point at the panel public IP — the
        autoconfig / autodiscover / mta-sts hostnames are added to the certificate
        only when they also resolve here, so it's fine to point them at a separate
        webmail host. Without this, clients have to connect via the panel hostname
        instead.
      </Typography.Paragraph>

      {cert.last_error && cert.status !== "issued" && (
        <Alert
          type={cert.status === "dns_missing" ? "warning" : "error"}
          message={cert.last_error}
          showIcon
          style={{ marginBottom: 12 }}
        />
      )}

      <Table
        size="small"
        rowKey="hostname"
        pagination={false}
        dataSource={cert.sans.map((h, idx) => ({ hostname: h, required: idx === 0 }))}
        columns={[
          {
            title: "Mail hostname",
            dataIndex: "hostname",
            render: (h: string) => <code>{h}</code>,
          },
          {
            title: "DNS A record",
            render: (_: unknown, row: { required: boolean }) =>
              row.required ? (
                <Typography.Text type="danger">required — point at panel public IP</Typography.Text>
              ) : (
                <Typography.Text type="secondary">optional — included if it resolves here</Typography.Text>
              ),
          },
        ]}
        style={{ marginBottom: 12 }}
      />

      {(cert.status === "failed" || cert.status === "dns_missing" || cert.status === "issued") && (
        <Tooltip title={t("domainmailtlssection.clears_backoff_and_dispatches_ssl_mail_issue")}>
          <Button icon={<ReloadOutlined />} loading={busy} onClick={handleReissue}>
            Re-issue
          </Button>
        </Tooltip>
      )}
    </div>
  );
};

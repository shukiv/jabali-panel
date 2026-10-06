// SSLTab — the SSL pane of the tenant Web Domain page (GH #1543, lxsdevcode).
// The Overview badge only tells you the state; this tab is where a tenant acts
// on the certificate for THIS domain: see its status / issuer / expiry and view
// the issued certificate. It reads the same owner-scoped endpoints the tenant
// SSL Manager table already uses (GET /domains/:id/ssl*), scoped to one domain.
//
// No Renew / Retry buttons: POST /domains/:id/ssl/renew and /ssl/retry are
// admin-only, so for a tenant they only ever failed with a 403. Renewal and
// retries run on their own; the tab says so.
//
// Deliberately NOT a certificate-mode switcher: choosing Let's Encrypt /
// self-signed / custom-upload / shared is an admin capability (DomainSSLSection
// hits /admin/*), so the mode is shown read-only here — same as the tenant SSL
// Manager, which lists the mode but never lets a tenant change it.
import { Alert, Button, Descriptions, Skeleton, Space, Tag, Typography } from "antd";
import { SafetyCertificateOutlined } from "@icons";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { getSSLTag } from "../../../../utils/sslState";
import { daysUntil, modeTag } from "../../../../components/ssl/sslHealth";
import { SSLCertViewModal } from "../../../../components/ssl/SSLCertViewModal";
import type { Domain } from "../../../../components/domains/types";

// The owner-scoped GET /domains/:id/ssl payload (mirrors the admin section's
// read — the API wraps the certificate in `.ssl`, and 404 means none issued).
type CertStatus = {
  status: string;
  issued_at?: string;
  expires_at?: string;
  last_error?: string;
  staging?: boolean;
};

export const SSLTab = ({ domain }: { domain: Domain }) => {
  const [viewOpen, setViewOpen] = useState(false);

  const certQ = useQuery<CertStatus | null>({
    queryKey: ["domain-ssl", domain.id],
    queryFn: async () => {
      try {
        const res = await apiClient.get<{ ssl: CertStatus }>(`/domains/${domain.id}/ssl`);
        return res.data.ssl;
      } catch (err) {
        // No certificate row yet (pre-issuance) reads as 404 — not an error.
        if ((err as { response?: { status?: number } }).response?.status === 404) return null;
        throw err;
      }
    },
  });

  if (certQ.isLoading) return <Skeleton active paragraph={{ rows: 3 }} />;

  const cert = certQ.data;
  const status = cert?.status;
  const ssl = getSSLTag(domain.ssl_state);
  const mode = modeTag(domain.ssl_mode);
  const isIssued = status === "issued";
  // A parked (pending_acme_retry) or failed cert is serving the self-signed
  // fallback while the server retries issuance on its own.
  const isRetrying = status === "failed" || status === "pending_acme_retry";
  const days = isIssued ? daysUntil(cert?.expires_at ?? null) : null;
  // Explain the state without implying "no certificate" — a self_signed domain
  // IS served. View Certificate stays issued-only because the API's inspect
  // endpoint returns a 409 for any non-issued status.
  const note = isIssued
    ? "The certificate renews automatically before it expires."
    : isRetrying
      ? "The server retries issuance automatically. Once the domain's DNS points at this server, the next retry gets the certificate; the server administrator can also retry it now."
      : status === "self_signed"
      ? "This domain is served with a self-signed certificate. The certificate mode is set by the server administrator."
      : status === "revoked"
        ? "The certificate was revoked. The certificate mode is set by the server administrator."
        : status === "pending" || status === "issuing" || status === "renewing"
          ? "A certificate is being issued — its details appear here once it's ready."
          : "SSL is managed by the server administrator. When a certificate is issued, its details appear here.";

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      {status === "failed" && cert?.last_error ? (
        <Alert type="error" showIcon message="Certificate issuance failed" description={cert.last_error} />
      ) : null}
      {status === "pending_acme_retry" && cert?.last_error ? (
        <Alert
          type="warning"
          showIcon
          message="Serving a self-signed certificate while issuance retries"
          description={cert.last_error}
        />
      ) : null}

      <Descriptions column={1} size="small" bordered>
        <Descriptions.Item label="SSL">
          <Space>
            <Tag color={ssl.color}>{ssl.label}</Tag>
            {cert?.staging ? <Tag color="purple">staging</Tag> : null}
          </Space>
        </Descriptions.Item>
        {mode ? (
          <Descriptions.Item label="Certificate mode">
            <Tag color={mode.color}>{mode.label}</Tag>
          </Descriptions.Item>
        ) : null}
        {domain.ssl?.issuer ? (
          <Descriptions.Item label="Issuer">{domain.ssl.issuer}</Descriptions.Item>
        ) : null}
        {isIssued && cert?.issued_at ? (
          <Descriptions.Item label="Issued">{new Date(cert.issued_at).toLocaleString()}</Descriptions.Item>
        ) : null}
        {isIssued && cert?.expires_at ? (
          <Descriptions.Item label="Expires">
            {new Date(cert.expires_at).toLocaleString()}
            {days !== null ? (
              <Tag color={days < 15 ? "orange" : "default"} style={{ marginLeft: 8 }}>
                in {days} day{days === 1 ? "" : "s"}
              </Tag>
            ) : null}
          </Descriptions.Item>
        ) : null}
      </Descriptions>

      {isIssued ? (
        <Space wrap>
          <Button icon={<SafetyCertificateOutlined />} onClick={() => setViewOpen(true)}>
            View Certificate
          </Button>
        </Space>
      ) : null}

      <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
        {note}
      </Typography.Paragraph>

      <SSLCertViewModal
        domainId={viewOpen ? domain.id : null}
        domainName={domain.name}
        onClose={() => setViewOpen(false)}
      />
    </Space>
  );
};

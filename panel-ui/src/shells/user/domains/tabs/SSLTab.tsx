// SSLTab — the SSL pane of the tenant Web Domain page (GH #1543, lxsdevcode).
// The Overview badge only tells you the state; this tab is where a tenant acts
// on the certificate for THIS domain: see its status / issuer / expiry, view
// the issued certificate, and renew or retry issuance. It reads the same
// owner-scoped endpoints the tenant SSL Manager table already uses
// (GET/POST /domains/:id/ssl*), scoped to one domain.
//
// Deliberately NOT a certificate-mode switcher: choosing Let's Encrypt /
// self-signed / custom-upload / shared is an admin capability (DomainSSLSection
// hits /admin/*), so the mode is shown read-only here — same as the tenant SSL
// Manager, which lists the mode but never lets a tenant change it.
import { Alert, Button, Descriptions, Skeleton, Space, Tag, Typography } from "antd";
import { ReloadOutlined, RedoOutlined, SafetyCertificateOutlined } from "@icons";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { feedback } from "../../../../lib/feedback";
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
  const qc = useQueryClient();
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

  // Renew/retry both change the served cert, so refresh this tab, the domain
  // row + list (the Overview badge), and the SSL Manager page in one go —
  // the same fan-out the Overview toggles use.
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["domain-ssl", domain.id] });
    qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
    qc.invalidateQueries({ queryKey: ["list", "domains"] });
    qc.invalidateQueries({ queryKey: ["ssl-manager"] });
  };

  const renew = useMutation({
    mutationFn: () => apiClient.post(`/domains/${domain.id}/ssl/renew`),
    onSuccess: () => {
      feedback.message.success("Renewal scheduled");
      invalidate();
    },
    onError: () => feedback.message.error("Failed to schedule renewal"),
  });

  const retry = useMutation({
    mutationFn: () => apiClient.post(`/domains/${domain.id}/ssl/retry`),
    onSuccess: () => {
      feedback.message.success("Retry queued");
      invalidate();
    },
    onError: (err) => {
      // A cert that isn't in a retryable state comes back 409 with a reason —
      // surface it as info, not a blanket failure (mirrors DomainSSLSection).
      const e = err as { response?: { status?: number; data?: { detail?: string } } };
      if (e.response?.status === 409) {
        feedback.message.info(e.response.data?.detail ?? "This certificate isn't retryable right now.");
      } else {
        feedback.message.error(e.response?.data?.detail ?? "Failed to queue retry");
      }
      invalidate();
    },
  });

  if (certQ.isLoading) return <Skeleton active paragraph={{ rows: 3 }} />;

  const cert = certQ.data;
  const status = cert?.status;
  const ssl = getSSLTag(domain.ssl_state);
  const mode = modeTag(domain.ssl_mode);
  const isIssued = status === "issued";
  // A parked (pending_acme_retry) or failed cert is serving the self-signed
  // fallback; offer a manual retry so a tenant who just fixed DNS can re-attempt
  // now instead of waiting for the daily recheck.
  const isRetryable = status === "failed" || status === "pending_acme_retry";
  const days = isIssued ? daysUntil(cert?.expires_at ?? null) : null;
  // When there's no actionable cert (not issued, not retryable), explain the
  // state without implying "no certificate" — a self_signed domain IS served.
  // View Certificate stays issued-only because the API's inspect endpoint
  // returns a 409 for any non-issued status.
  const note =
    status === "self_signed"
      ? "This domain is served with a self-signed certificate. The certificate mode is set by the server administrator."
      : status === "revoked"
        ? "The certificate was revoked. The certificate mode is set by the server administrator."
        : status === "pending" || status === "issuing" || status === "renewing"
          ? "A certificate is being issued — its details and renewal actions appear here once it's ready."
          : "SSL is managed by the server administrator. When a certificate is issued, its details and renewal actions appear here.";

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

      <Space wrap>
        {isIssued ? (
          <>
            <Button icon={<SafetyCertificateOutlined />} onClick={() => setViewOpen(true)}>
              View Certificate
            </Button>
            <Button icon={<ReloadOutlined />} loading={renew.isPending} onClick={() => renew.mutate()}>
              Renew now
            </Button>
          </>
        ) : null}
        {isRetryable ? (
          <Button
            type="primary"
            icon={<RedoOutlined />}
            loading={retry.isPending}
            onClick={() => retry.mutate()}
          >
            Retry now
          </Button>
        ) : null}
      </Space>

      {!isIssued && !isRetryable ? (
        <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
          {note}
        </Typography.Paragraph>
      ) : null}

      <SSLCertViewModal
        domainId={viewOpen ? domain.id : null}
        domainName={domain.name}
        onClose={() => setViewOpen(false)}
      />
    </Space>
  );
};

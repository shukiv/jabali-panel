import { useTranslation } from "react-i18next";
import { useCallback, useEffect, useState } from "react";
import { Alert, Button, Input, Modal, Select, Skeleton, Space, Tag, Tooltip, message } from "antd";
import { ReloadOutlined } from "@icons";
import { Link } from "react-router";

import { apiClient } from "../../../apiClient";

type SSLStatus =
  | "pending"
  | "issuing"
  | "issued"
  | "failed"
  | "revoked"
  | "renewing"
  | "self_signed"
  | "pending_acme_retry";

type SSLCertificate = {
  status: SSLStatus;
  issued_at?: string;
  expires_at?: string;
  renewal_count: number;
  last_renewed_at?: string;
  last_error?: string;
  staging: boolean;
  cert_path?: string;
  key_path?: string;
  next_retry_at?: string;
  retry_count: number;
};

type ServerSettings = {
  admin_email?: string;
};

const STATUS_COLORS: Record<SSLStatus, string> = {
  pending: "default",
  issuing: "processing",
  renewing: "processing",
  issued: "green",
  failed: "red",
  revoked: "default",
  self_signed: "orange",
  pending_acme_retry: "gold",
};

function daysUntil(iso?: string): number | null {
  if (!iso) return null;
  const diff = new Date(iso).getTime() - Date.now();
  return Math.round(diff / 86_400_000);
}

type Props = {
  domainId: string;
  sslEnabled: boolean;
  onToggled: () => void;
};

export const DomainSSLSection = ({ domainId, sslEnabled, onToggled }: Props) => {
  const { t } = useTranslation();
  const [cert, setCert] = useState<SSLCertificate | null>(null);
  const [certMissing, setCertMissing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [renewing, setRenewing] = useState(false);
  const [adminEmail, setAdminEmail] = useState<string | undefined>(undefined);
  const [sslMode, setSslMode] = useState<string>("le");
  const [modeChanging, setModeChanging] = useState(false);
  const [customOpen, setCustomOpen] = useState(false);
  const [certPem, setCertPem] = useState("");
  const [keyPem, setKeyPem] = useState("");
  const [uploading, setUploading] = useState(false);

  const fetchCert = useCallback(async () => {
    setLoading(true);
    try {
      const res = await apiClient.get(`/domains/${domainId}/ssl`);
      setCert(res.data.ssl);
      setCertMissing(false);
    } catch (err: unknown) {
      const status = (err as { response?: { status?: number } })?.response?.status;
      if (status === 404) {
        setCert(null);
        setCertMissing(true);
      } else {
        message.error("Failed to load SSL status");
      }
    } finally {
      setLoading(false);
    }
  }, [domainId]);

  useEffect(() => {
    fetchCert();
    apiClient
      .get<{ settings: ServerSettings }>("/system/settings")
      .then((res) => setAdminEmail(res.data?.settings?.admin_email))
      .catch(() => undefined);
    apiClient
      .get<{ ssl_mode?: string }>(`/domains/${domainId}`)
      .then((res) => setSslMode(res.data?.ssl_mode ?? "le"))
      .catch(() => undefined);
  }, [fetchCert, domainId]);


  const applyMode = async (mode: string) => {
    if (mode === "custom") {
      setCustomOpen(true);
      return;
    }
    setModeChanging(true);
    try {
      await apiClient.patch(`/domains/${domainId}`, { ssl_mode: mode });
      setSslMode(mode);
      message.success("Certificate mode updated");
      onToggled();
      await fetchCert();
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
      message.error(resp?.detail ?? "Failed to change certificate mode");
    } finally {
      setModeChanging(false);
    }
  };

  const uploadCustom = async () => {
    if (!certPem.trim() || !keyPem.trim()) {
      message.error("Paste both the certificate and the private key");
      return;
    }
    setUploading(true);
    try {
      await apiClient.put(`/domains/${domainId}/ssl/custom`, {
        cert_pem: certPem,
        key_pem: keyPem,
      });
      setSslMode("custom");
      setCustomOpen(false);
      setCertPem("");
      setKeyPem("");
      message.success("Custom certificate installed");
      onToggled();
      await fetchCert();
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
      message.error(resp?.detail ?? "Failed to install custom certificate");
    } finally {
      setUploading(false);
    }
  };

  const onRenew = async () => {
    setRenewing(true);
    try {
      await apiClient.post(`/domains/${domainId}/ssl/renew`);
      message.success("Renewal scheduled");
      await fetchCert();
    } catch {
      message.error("Failed to schedule renewal");
    } finally {
      setRenewing(false);
    }
  };

  const onRetry = async () => {
    setRenewing(true);
    try {
      await apiClient.post(`/domains/${domainId}/ssl/retry`);
      message.success("Retry queued");
      await fetchCert();
    } catch {
      message.error("Failed to queue retry");
    } finally {
      setRenewing(false);
    }
  };

  if (loading) return <Skeleton active paragraph={{ rows: 2 }} />;

  const status = cert?.status;
  const days = daysUntil(cert?.expires_at);

  return (
    <Space direction="vertical" style={{ width: "100%" }}>
      {!adminEmail && (
        <Alert
          type="warning"
          showIcon
          title={t("domainsslsection.set_an_admin_email_to_use_ssl")}
          description={
            <>
              Let&apos;s Encrypt requires an email on the account. Configure it in{" "}
              <Link to="/jabali-admin/settings">Server Settings</Link>.
            </>
          }
        />
      )}

      {status === "self_signed" || status === "pending_acme_retry" ? (
        <Alert
          type="warning"
          showIcon
          title={t("domainsslsection.using_self_signed_certificate")}
          description={t("domainsslsection.this_domain_is_using_a_self_signed_certifica")}
        />
      ) : null}

      {status === "failed" ? (
        <Alert
          type="error"
          showIcon
          title={t("domainsslsection.acme_issuance_failed")}
          description={
            <>
              {cert?.last_error && <div>Error: {cert.last_error}</div>}
              <Button
                type="primary"
                loading={renewing}
                onClick={onRetry}
                style={{ marginTop: 8 }}
              >
                Retry
              </Button>
            </>
          }
        />
      ) : null}

      <Space size="middle" align="center">
        <Select
          value={sslMode}
          loading={modeChanging}
          style={{ width: 220 }}
          onChange={applyMode}
          options={[
            { value: "le", label: "Let's Encrypt", disabled: !adminEmail && sslMode !== "le" },
            { value: "self", label: "Self-signed" },
            { value: "custom", label: "Custom (upload)" },
            { value: "none", label: "None (HTTP only)" },
          ]}
        />
        <span>TLS certificate</span>

        {certMissing && sslEnabled && (
          <Tag color="default">pending issuance</Tag>
        )}
        {status && (
          <Tooltip title={status === "failed" ? cert?.last_error : undefined}>
            <Tag color={STATUS_COLORS[status]}>
              {status.replace(/_/g, " ")}
            </Tag>
          </Tooltip>
        )}
        {status === "issued" && days !== null && (
          <Tag color={days < 15 ? "orange" : "default"}>
            expires in {days} day{days === 1 ? "" : "s"}
          </Tag>
        )}
        {cert?.staging && <Tag color="purple">staging</Tag>}

        {status === "issued" && (
          <Button
            icon={<ReloadOutlined />}
            loading={renewing}
            onClick={onRenew}
          >
            Renew now
          </Button>
        )}
      </Space>

      <Modal
        open={customOpen}
        title={t("domainsslsection.upload_custom_certificate")}
        okText={t("domainsslsection.install")}
        confirmLoading={uploading}
        onOk={uploadCustom}
        onCancel={() => setCustomOpen(false)}
        width={640}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Alert
            type="info"
            showIcon
            message={t("domainsslsection.paste_the_full_chain_certificate_leaf_interm")}
          />
          <div>
            <div style={{ marginBottom: 4 }}>Certificate (PEM)</div>
            <Input.TextArea
              rows={6}
              value={certPem}
              onChange={(e) => setCertPem(e.target.value)}
              placeholder="-----BEGIN CERTIFICATE-----"
              style={{ fontFamily: "monospace" }}
            />
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>Private key (PEM)</div>
            <Input.TextArea
              rows={6}
              value={keyPem}
              onChange={(e) => setKeyPem(e.target.value)}
              placeholder="-----BEGIN PRIVATE KEY-----"
              style={{ fontFamily: "monospace" }}
            />
          </div>
        </Space>
      </Modal>
    </Space>
  );
};

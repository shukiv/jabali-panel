import { useTranslation } from "react-i18next";
import { useCallback, useEffect, useState } from "react";
import { Alert, Button, Input, Modal, Select, Skeleton, Space, Tag, Tooltip } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
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
  // GH #1221: configured SANs the issued cert doesn't cover yet (no public DNS
  // at issuance); added automatically once they resolve.
  pending_sans?: string[];
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
  domainName: string;
  sslEnabled: boolean;
  onToggled: () => void;
};

// Shared wildcard/multi-SAN certificate (JAB-170) — a server-wide or
// tenant-owned cert an admin uploaded once via the SSL Manager page.
type SharedCert = {
  id: string;
  name: string;
  sans: string[];
  expires_at?: string;
};

// hostMatchesSAN mirrors the API's wildcard-aware matcher (x509.VerifyHostname
// semantics): exact, or a single-label wildcard. The server re-checks on
// attach; this just filters the picker to covering certs.
function hostMatchesSAN(san: string, host: string): boolean {
  const s = san.trim().toLowerCase();
  const h = host.trim().toLowerCase();
  if (!s || !h) return false;
  if (s === h) return true;
  if (s.startsWith("*.")) {
    const base = s.slice(2);
    const dot = h.indexOf(".");
    if (dot > 0 && h.slice(dot + 1) === base) return true;
  }
  return false;
}

const certCoversHost = (cert: SharedCert, host: string): boolean =>
  (cert.sans ?? []).some((s) => hostMatchesSAN(s, host));

export const DomainSSLSection = ({ domainId, domainName, sslEnabled, onToggled }: Props) => {
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
  const [sharedCertId, setSharedCertId] = useState<string | undefined>(undefined);
  const [sharedPickerOpen, setSharedPickerOpen] = useState(false);
  const [covering, setCovering] = useState<SharedCert[]>([]);
  const [selectedShared, setSelectedShared] = useState<string | undefined>(undefined);
  const [sharedBusy, setSharedBusy] = useState(false);

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
        feedback.message.error("Failed to load SSL status");
      }
    } finally {
      setLoading(false);
    }
  }, [domainId]);

  useEffect(() => {
    fetchCert();
    // GH #738: admin_email lives at GET /admin/settings as a FLAT field. The
    // old /system/settings path 404'd (no such route) and the .settings.*
    // nesting was wrong too, so adminEmail was ALWAYS undefined — which left
    // the "Let's Encrypt" option greyed out even when an admin email was set
    // (the exact symptom in #738). Read the real endpoint + flat field.
    apiClient
      .get<ServerSettings>("/admin/settings")
      .then((res) => setAdminEmail(res.data?.admin_email))
      .catch(() => undefined);
    apiClient
      .get<{ ssl_mode?: string; shared_certificate_id?: string }>(`/domains/${domainId}`)
      .then((res) => {
        setSslMode(res.data?.ssl_mode ?? "le");
        setSharedCertId(res.data?.shared_certificate_id);
      })
      .catch(() => undefined);
  }, [fetchCert, domainId]);


  const openSharedPicker = async () => {
    setSelectedShared(undefined);
    setSharedPickerOpen(true);
    try {
      const res = await apiClient.get("/admin/certificates/shared");
      const all = (res.data?.data ?? []) as SharedCert[];
      setCovering(all.filter((c) => certCoversHost(c, domainName)));
    } catch {
      setCovering([]);
      feedback.message.error("Failed to load shared certificates");
    }
  };

  const attachShared = async () => {
    if (!selectedShared) {
      feedback.message.error("Choose a shared certificate");
      return;
    }
    setSharedBusy(true);
    try {
      await apiClient.post(`/domains/${domainId}/ssl/shared`, {
        shared_certificate_id: selectedShared,
      });
      setSslMode("shared");
      setSharedCertId(selectedShared);
      setSharedPickerOpen(false);
      feedback.message.success("Shared certificate attached");
      onToggled();
      await fetchCert();
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
      feedback.message.error(resp?.detail ?? "Failed to attach shared certificate");
    } finally {
      setSharedBusy(false);
    }
  };

  const detachShared = async () => {
    setSharedBusy(true);
    try {
      await apiClient.delete(`/domains/${domainId}/ssl/shared`);
      setSslMode("le");
      setSharedCertId(undefined);
      feedback.message.success("Shared certificate detached (reverted to Let's Encrypt)");
      onToggled();
      await fetchCert();
    } catch {
      feedback.message.error("Failed to detach shared certificate");
    } finally {
      setSharedBusy(false);
    }
  };

  const applyMode = async (mode: string) => {
    if (mode === "custom") {
      setCustomOpen(true);
      return;
    }
    if (mode === "shared") {
      await openSharedPicker();
      return;
    }
    setModeChanging(true);
    try {
      await apiClient.patch(`/domains/${domainId}`, { ssl_mode: mode });
      setSslMode(mode);
      feedback.message.success("Certificate mode updated");
      onToggled();
      await fetchCert();
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
      feedback.message.error(resp?.detail ?? "Failed to change certificate mode");
    } finally {
      setModeChanging(false);
    }
  };

  const uploadCustom = async () => {
    if (!certPem.trim() || !keyPem.trim()) {
      feedback.message.error("Paste both the certificate and the private key");
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
      feedback.message.success("Custom certificate installed");
      onToggled();
      await fetchCert();
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
      feedback.message.error(resp?.detail ?? "Failed to install custom certificate");
    } finally {
      setUploading(false);
    }
  };

  const onRenew = async () => {
    setRenewing(true);
    try {
      await apiClient.post(`/domains/${domainId}/ssl/renew`);
      feedback.message.success("Renewal scheduled");
      await fetchCert();
    } catch {
      feedback.message.error("Failed to schedule renewal");
    } finally {
      setRenewing(false);
    }
  };

  const onRetry = async () => {
    setRenewing(true);
    try {
      await apiClient.post(`/domains/${domainId}/ssl/retry`);
      feedback.message.success("Retry queued");
      await fetchCert();
    } catch (err) {
      // GH #1221: surface the server's real reason (e.g. the cert is already
      // being issued) instead of a blanket "failed", and refetch so the panel
      // reflects the true status.
      const e = err as { response?: { status?: number; data?: { detail?: string } } };
      const detail = e.response?.data?.detail;
      if (e.response?.status === 409) {
        feedback.message.info(detail ?? "This certificate isn't in a retryable state right now.");
      } else {
        feedback.message.error(detail ?? "Failed to queue retry");
      }
      await fetchCert();
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
          description={
            <>
              {t("domainsslsection.this_domain_is_using_a_self_signed_certifica")}
              {/* GH #1221: a parked (pending_acme_retry) cert is serving the
                  self-signed fallback while Let's Encrypt retries on a slow
                  cadence. Surface the reason + a manual Retry so an operator
                  who just fixed DNS/delegation can re-attempt immediately,
                  instead of waiting for the daily recheck. */}
              {status === "pending_acme_retry" && (
                <>
                  {cert?.last_error && (
                    <div style={{ marginTop: 8 }}>Reason: {cert.last_error}</div>
                  )}
                  <Button
                    type="primary"
                    loading={renewing}
                    onClick={onRetry}
                    style={{ marginTop: 8 }}
                  >
                    Retry now
                  </Button>
                </>
              )}
            </>
          }
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
            { value: "shared", label: "Shared certificate" },
            { value: "none", label: "None (HTTP only)" },
          ]}
        />
        <span>TLS certificate</span>

        {sslMode === "shared" && (
          <>
            <Tooltip title={sharedCertId ? `Certificate ${sharedCertId}` : undefined}>
              <Tag color="geekblue">shared</Tag>
            </Tooltip>
            <Button
              size="small"
              loading={sharedBusy}
              onClick={detachShared}
            >
              Detach
            </Button>
          </>
        )}

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

      {/* GH #1221: an issued cert can legitimately be missing some configured
          SANs — Let's Encrypt validation drops names with no public DNS at
          issuance so one unresolvable helper (e.g. autoconfig) can't fail the
          whole cert. Tell the operator, so a partial cert doesn't read as a bug;
          the drift pass re-adds them once their DNS resolves. */}
      {status === "issued" && (cert?.pending_sans?.length ?? 0) > 0 && (
        <Alert
          type="info"
          showIcon
          style={{ marginTop: 12 }}
          message={`Not on the certificate yet: ${cert!.pending_sans!.join(", ")}`}
          description="These hostnames don't resolve publicly yet, so Let's Encrypt couldn't validate them at issuance. They're added to the certificate automatically once their DNS resolves — no action needed."
        />
      )}

      {/* GH #738: the "Let's Encrypt" option greys out with no explanation when
          no admin email is set (it's the required ACME contact). Tell the
          operator why + where to fix it, so they aren't stuck. */}
      {!adminEmail && sslMode !== "le" && (
        <Alert
          type="info"
          showIcon
          style={{ marginTop: 12 }}
          message={
            <>
              Let&apos;s Encrypt needs an admin email (the ACME contact address).
              Set one in <Link to="/jabali-admin/settings">Server Settings</Link>,
              then select Let&apos;s Encrypt here.
            </>
          }
        />
      )}

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

      <Modal
        open={sharedPickerOpen}
        title={t("domainsslsection.attach_shared_certificate")}
        okText={t("domainsslsection.attach")}
        okButtonProps={{ disabled: !selectedShared }}
        confirmLoading={sharedBusy}
        onOk={attachShared}
        onCancel={() => setSharedPickerOpen(false)}
        width={560}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Alert
            type="info"
            showIcon
            message={t("domainsslsection.only_shared_certificates_that_cover_this_dom")}
          />
          {covering.length === 0 ? (
            <Alert
              type="warning"
              showIcon
              message={
                <>
                  No shared certificate covers <strong>{domainName}</strong>. Upload
                  one from{" "}
                  <Link to="/jabali-admin/ssl">SSL Manager</Link>.
                </>
              }
            />
          ) : (
            <Select
              value={selectedShared}
              style={{ width: "100%" }}
              placeholder="Choose a covering certificate"
              onChange={(v) => setSelectedShared(v)}
              options={covering.map((c) => ({
                value: c.id,
                label: `${c.name} — ${(c.sans ?? []).join(", ")}`,
              }))}
            />
          )}
        </Space>
      </Modal>
    </Space>
  );
};

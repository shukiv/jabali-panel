// PanelSSLCard — Server Settings → General: one status line per panel cert
// (M32, ADR-0105) and a link to SSL Manager → Panel SSL, where they are
// managed (GH #2166). The full card — switches, details, actions — moved
// there; this keeps the certs in sight next to the hostname they serve.
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { SafetyOutlined } from "@icons";
import { Alert, Card, Space, Typography } from "antd";
import { type PanelCertificate, usePanelCertificate } from "../../../hooks/usePanelCertificate";
import {
  PANEL_SSL_TAB,
  panelCertExpiryHint,
  panelCertStatusTag,
} from "../../../components/ssl/panelCertStatus";

function CertLine({ label, cert }: { label: string; cert: PanelCertificate }) {
  const expiry = panelCertExpiryHint(cert);
  return (
    <Space size={8} wrap>
      <Typography.Text strong style={{ minWidth: 72, display: "inline-block" }}>
        {label}
      </Typography.Text>
      <code>{cert.hostname || "<unset>"}</code>
      {panelCertStatusTag(cert)}
      {expiry && <Typography.Text type={expiry === "Expired" ? "danger" : "secondary"}>{expiry}</Typography.Text>}
    </Space>
  );
}

export function PanelSSLCard() {
  const { t } = useTranslation();
  const q = usePanelCertificate();
  const host = q.data?.find((c) => c.kind === "hostname") ?? q.data?.[0];
  const mail = q.data?.find((c) => c.kind === "mail");

  return (
    <Card
      title={
        <Space>
          <SafetyOutlined />
          <span>{t("panelsslcard.panel_ssl")}</span>
        </Space>
      }
      extra={<Link to={PANEL_SSL_TAB}>Manage in SSL Manager</Link>}
      loading={q.isPending}
      style={{ marginBottom: 16 }}
    >
      <Space orientation="vertical" size={8} style={{ width: "100%" }}>
        {q.isError ? (
          <Alert
            type="error"
            showIcon
            title={t("panelsslcard.failed_to_load_panel_ssl_state")}
            description={String((q.error as Error)?.message ?? "")}
          />
        ) : host ? (
          <>
            <CertLine label={t("panelsslcard.hostname")} cert={host} />
            {mail && <CertLine label={t("panelsslcard.mail")} cert={mail} />}
          </>
        ) : (
          <Alert type="info" showIcon title={t("panelsslcard.panel_certificate_not_initialised_yet")} />
        )}
        <Typography.Text type="secondary">
          The panel&apos;s certificates — Let&apos;s Encrypt, details and actions — are managed in{" "}
          <Link to={PANEL_SSL_TAB}>SSL Manager &rarr; Panel SSL</Link>.
        </Typography.Text>
      </Space>
    </Card>
  );
}

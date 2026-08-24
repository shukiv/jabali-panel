// PanelSSLCard — admin panel for the panel's TLS certs (M32, ADR-0105).
//
// Post-split there are TWO independent certs: the panel hostname cert
// and the panel mail (mail.<hostname>) cert. Each gets its own status
// row + Retry; mail can never block the hostname cert. The single
// Use-LE / staging toggle (the hostname row's flags) governs both.
import { useTranslation } from "react-i18next";
import {
  CheckCircleOutlined,
  CloseOutlined,
  ReloadOutlined,
  SafetyOutlined,
} from "@icons";
import { Alert, Button, Card, Popconfirm, Space, Switch, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  type PanelCertKind,
  type PanelCertificate,
  usePanelCertificate,
  usePanelCertificateIssue,
  usePanelCertificateToggle,
} from "../../../hooks/usePanelCertificate";
import {
  panelCertExpiryHint,
  panelCertStatusTag,
} from "../../../components/ssl/panelCertStatus";

function CertRow({
  label,
  cert,
  onRetry,
  retrying,
}: {
  label: string;
  cert: PanelCertificate;
  onRetry: () => void;
  retrying: boolean;
}) {
  const expiry = panelCertExpiryHint(cert);
  return (
    <div style={{ borderTop: "1px solid rgba(255,255,255,0.08)", paddingTop: 12 }}>
      <Space direction="vertical" size={6} style={{ width: "100%" }}>
        <Space wrap>
          <Typography.Text strong style={{ minWidth: 72, display: "inline-block" }}>
            {label}
          </Typography.Text>
          <code>{cert.hostname || "<unset>"}</code>
        </Space>
        <Space wrap>
          {panelCertStatusTag(cert)}
          {cert.routable ? (
            <Tag icon={<CheckCircleOutlined />} color="success">
              Routable
            </Tag>
          ) : (
            <Tag
              icon={<CloseOutlined />}
              style={{ whiteSpace: "normal", maxWidth: "100%", height: "auto" }}
            >
              Not routable
              {cert.routable_reason ? ` — ${cert.routable_reason}` : ""}
            </Tag>
          )}
          {expiry && (
            <Tag color={expiry === "Expired" ? "error" : undefined}>{expiry}</Tag>
          )}
          <Popconfirm
            title={`Issue the ${label.toLowerCase()} cert now?`}
            onConfirm={onRetry}
          >
            <Button size="small" icon={<ReloadOutlined />} loading={retrying}>
              Issue / retry
            </Button>
          </Popconfirm>
        </Space>
        {(cert.status === "pending_acme_retry" || cert.status === "failed") &&
          cert.last_error && (
            <Alert
              type="warning"
              showIcon
              message={`${label} — last attempt failed (attempt ${cert.attempt_count})`}
              description={cert.last_error}
            />
          )}
      </Space>
    </div>
  );
}

export function PanelSSLCard() {
  const { t } = useTranslation();
  const q = usePanelCertificate();
  const toggle = usePanelCertificateToggle();
  const issue = usePanelCertificateIssue();

  if (q.isPending) {
    return <Card title={t("panelsslcard.panel_ssl")} loading style={{ marginBottom: 16 }} />;
  }
  if (q.isError || !q.data) {
    return (
      <Card title={t("panelsslcard.panel_ssl")} style={{ marginBottom: 16 }}>
        <Alert
          type="error"
          message={t("panelsslcard.failed_to_load_panel_ssl_state")}
          description={String((q.error as Error)?.message ?? "")}
          showIcon
        />
      </Card>
    );
  }
  const certs = q.data;
  const host =
    certs.find((c) => c.kind === "hostname") ?? certs[0];
  const mail = certs.find((c) => c.kind === "mail");
  if (!host) {
    return (
      <Card title={t("panelsslcard.panel_ssl")} style={{ marginBottom: 16 }}>
        <Alert type="info" message={t("panelsslcard.panel_certificate_not_initialised_yet")} showIcon />
      </Card>
    );
  }

  const doIssue = (kind: PanelCertKind) =>
    issue.mutate(kind, {
      onSuccess: () => feedback.message.success(`${kind} cert: issued`),
      onError: (e) =>
        feedback.message.error(`${`${kind} cert: issue failed`}: ${String((e as Error).message)}`),
    });

  return (
    <Card
      title={
        <Space>
          <SafetyOutlined />
          <span>Panel SSL</span>
        </Space>
      }
      style={{ marginBottom: 16 }}
      extra={
        <Button
          icon={<ReloadOutlined />}
          size="small"
          onClick={() => q.refetch()}
        >
          Refresh
        </Button>
      }
    >
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
          Two independent Let&apos;s Encrypt certs for the panel:{" "}
          <code>{host.hostname || "<hostname>"}</code> (panel hostname) and{" "}
          <code>{mail?.hostname || `mail.${host.hostname || "<hostname>"}`}</code>{" "}
          (mail). Each issues + retries on its own — the mail cert never
          blocks the hostname cert. Self-signed remains the fallback.
        </Typography.Paragraph>

        {!host.routable && (
          <Alert
            type="warning"
            showIcon
            message={t("panelsslcard.let_s_encrypt_is_unavailable_for_the_panel")}
            description={
              host.routable_reason
                ? `${host.routable_reason}. Point the panel hostname's DNS A record at this server's public IP, then click Refresh.`
                : "The panel hostname must resolve publicly to this server's IP. Point its DNS A record at the public IP, then click Refresh."
            }
          />
        )}
        <Space wrap>
          <Switch
            checked={host.use_le}
            disabled={!host.routable && !host.use_le}
            loading={toggle.isPending}
            onChange={(v) => {
              toggle.mutate(
                { use_le: v },
                {
                  onSuccess: () =>
                    feedback.message.success(v
                        ? "Let's Encrypt enabled — issuance runs on the next reconciler tick"
                        : "Let's Encrypt disabled — existing certs stay until expiry"),
                  onError: (e) =>
                    feedback.message.error(`Failed to update toggle: ${String((e as Error).message)}`),
                },
              );
            }}
          />
          <Typography.Text>Use Let&apos;s Encrypt for the panel</Typography.Text>
        </Space>

        <Space wrap>
          <Switch
            checked={host.staging}
            disabled={!host.use_le}
            loading={toggle.isPending}
            onChange={(v) => toggle.mutate({ staging: v })}
          />
          <Typography.Text>
            Use Let&apos;s Encrypt staging (testing only — browsers will warn
            about the test cert)
          </Typography.Text>
        </Space>

        <CertRow
          label={t("panelsslcard.hostname")}
          cert={host}
          retrying={issue.isPending}
          onRetry={() => doIssue("hostname")}
        />
        {mail && (
          <CertRow
            label={t("panelsslcard.mail")}
            cert={mail}
            retrying={issue.isPending}
            onRetry={() => doIssue("mail")}
          />
        )}
      </Space>
    </Card>
  );
}

// PanelSSLTab — SSL Manager → Panel SSL (GH #2166). The panel's own two
// certs (ADR-0105), the panel hostname and the mail name, each with the
// certificate its file holds (type, issuer, names, validity, fingerprint),
// its state and its action, plus the Use-LE / staging switches: the hostname
// row's flags govern both certs. It replaced the compact SYSTEM band above
// the SSL Manager tabs and the full card on Server Settings, which now
// links here.
import { useTranslation } from "react-i18next";
import { CheckCircleOutlined, CloseOutlined, ReloadOutlined } from "@icons";
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Popconfirm,
  Skeleton,
  Space,
  Switch,
  Tag,
  Typography,
} from "antd";
import type { DescriptionsProps } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  isPanelSelfSignResult,
  type PanelCertKind,
  type PanelCertificate,
  usePanelCertificate,
  usePanelCertificateIssue,
  usePanelCertificateToggle,
} from "../../hooks/usePanelCertificate";
import { shortDate, shortDateTime } from "../../utils/datetime";
import {
  panelCertActionConfirm,
  panelCertActionLabel,
  panelCertIssueMessage,
  panelCertStatusTag,
  panelCertTypeLabel,
  panelCertValidity,
} from "./panelCertStatus";

function servedItems(cert: PanelCertificate): DescriptionsProps["items"] {
  const s = cert.served;
  const items: DescriptionsProps["items"] = [
    {
      key: "status",
      label: "Status",
      children: (
        <Space size={4} wrap>
          {panelCertStatusTag(cert)}
          {cert.routable ? (
            <Tag icon={<CheckCircleOutlined />} color="success">
              Routable
            </Tag>
          ) : (
            <Tag icon={<CloseOutlined />} style={{ whiteSpace: "normal", maxWidth: "100%", height: "auto" }}>
              Not routable
              {cert.routable_reason ? ` — ${cert.routable_reason}` : ""}
            </Tag>
          )}
        </Space>
      ),
    },
  ];
  if (s) {
    const validity = panelCertValidity(s);
    items.push(
      { key: "type", label: "Type", children: panelCertTypeLabel(s) },
      { key: "issuer", label: "Issuer", children: s.issuer || "—" },
      {
        key: "names",
        label: "Names",
        span: "filled",
        children: (
          <Space size={4} wrap>
            {s.names.map((n) => (
              <Tag key={n}>
                <code>{n}</code>
              </Tag>
            ))}
          </Space>
        ),
      },
      { key: "from", label: "Valid from", children: shortDate(s.not_before) },
      {
        key: "until",
        label: "Valid until",
        children: (
          <Space size={4} wrap>
            <span>{validity.until}</span>
            {validity.tag}
          </Space>
        ),
      },
      {
        key: "fingerprint",
        label: "SHA-256 fingerprint",
        span: "filled",
        children: (
          <Typography.Text code copyable style={{ fontSize: 12, wordBreak: "break-all" }}>
            {s.fingerprint_sha256}
          </Typography.Text>
        ),
      },
    );
  }
  if (cert.next_retry_at) {
    items.push({ key: "retry", label: "Next retry", children: shortDateTime(cert.next_retry_at) });
  }
  return items;
}

function CertCard({
  label,
  cert,
  onIssue,
  issuing,
}: {
  label: string;
  cert: PanelCertificate;
  onIssue: () => void;
  issuing: boolean;
}) {
  const confirm = panelCertActionConfirm(cert, label);
  return (
    <Card
      size="small"
      type="inner"
      title={
        <Space wrap>
          <span>{label}</span>
          <code>{cert.hostname || "<unset>"}</code>
        </Space>
      }
      extra={
        <Popconfirm title={confirm.title} description={confirm.description} onConfirm={onIssue}>
          <Button size="small" icon={<ReloadOutlined />} loading={issuing}>
            {panelCertActionLabel(cert)}
          </Button>
        </Popconfirm>
      }
    >
      <Space orientation="vertical" size={8} style={{ width: "100%" }}>
        <Descriptions size="small" bordered column={{ xs: 1, sm: 1, md: 2 }} items={servedItems(cert)} />
        {cert.served_error && <Alert type="warning" showIcon title={cert.served_error} />}
        {(cert.status === "pending_acme_retry" || cert.status === "failed") && cert.last_error && (
          <Alert
            type="warning"
            showIcon
            title={`${label} — last attempt failed (attempt ${cert.attempt_count})`}
            description={cert.last_error}
          />
        )}
      </Space>
    </Card>
  );
}

export function PanelSSLTab() {
  const { t } = useTranslation();
  const q = usePanelCertificate();
  const toggle = usePanelCertificateToggle();
  const issue = usePanelCertificateIssue();

  if (q.isPending) {
    return <Skeleton active />;
  }
  if (q.isError || !q.data) {
    return (
      <Alert
        type="error"
        showIcon
        title={t("panelsslcard.failed_to_load_panel_ssl_state")}
        description={String((q.error as Error)?.message ?? "")}
      />
    );
  }
  const host = q.data.find((c) => c.kind === "hostname") ?? q.data[0];
  const mail = q.data.find((c) => c.kind === "mail");
  if (!host) {
    return <Alert type="info" showIcon title={t("panelsslcard.panel_certificate_not_initialised_yet")} />;
  }
  const hostName = host.hostname || "<hostname>";
  const mailName = mail?.hostname || `mail.${hostName}`;

  const doIssue = (kind: PanelCertKind) =>
    issue.mutate(kind, {
      onSuccess: (r) =>
        feedback.message.success(panelCertIssueMessage(kind, r), isPanelSelfSignResult(r) ? 10 : undefined),
      onError: (e) => feedback.message.error(`${kind} cert: issue failed: ${String((e as Error).message)}`),
    });

  return (
    <Space orientation="vertical" size={12} style={{ width: "100%" }}>
      <Space wrap style={{ width: "100%", justifyContent: "space-between" }}>
        <Space wrap>
          <Switch
            checked={host.use_le}
            disabled={!host.routable && !host.use_le}
            loading={toggle.isPending}
            onChange={(v) =>
              toggle.mutate(
                { use_le: v },
                {
                  onSuccess: () =>
                    feedback.message.success(
                      v
                        ? "Let's Encrypt enabled — issuance runs on the next reconciler tick"
                        : "Let's Encrypt disabled — existing certs stay until expiry",
                    ),
                  onError: (e) => feedback.message.error(`Failed to update toggle: ${String((e as Error).message)}`),
                },
              )
            }
          />
          <Typography.Text>Use Let&apos;s Encrypt for the panel</Typography.Text>
          <Switch
            checked={host.staging}
            disabled={!host.use_le}
            loading={toggle.isPending}
            onChange={(v) => toggle.mutate({ staging: v })}
          />
          <Typography.Text>Staging (testing only — browsers warn about the test cert)</Typography.Text>
        </Space>
        <Button icon={<ReloadOutlined />} size="small" onClick={() => q.refetch()}>
          Refresh
        </Button>
      </Space>

      <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
        {host.use_le ? (
          <>
            Let&apos;s Encrypt issues and renews two independent certificates for the panel:{" "}
            <code>{hostName}</code> (panel hostname) and <code>{mailName}</code> (mail). Each issues and retries on
            its own; the mail cert never blocks the hostname cert.
          </>
        ) : (
          <>
            Let&apos;s Encrypt is off: the panel serves its own self-signed certificate for <code>{hostName}</code>,
            and browsers ask you to accept it. A certificate from Let&apos;s Encrypt or another authority is kept
            until it expires. The mail cert for <code>{mailName}</code> stays a Let&apos;s Encrypt action.
          </>
        )}
      </Typography.Paragraph>

      {!host.routable && (
        <Alert
          type="warning"
          showIcon
          title={t("panelsslcard.let_s_encrypt_is_unavailable_for_the_panel")}
          description={
            host.routable_reason
              ? `${host.routable_reason}. Point the panel hostname's DNS A record at this server's public IP, then click Refresh.`
              : "The panel hostname must resolve publicly to this server's IP. Point its DNS A record at the public IP, then click Refresh."
          }
        />
      )}

      <CertCard
        label={t("panelsslcard.hostname")}
        cert={host}
        issuing={issue.isPending}
        onIssue={() => doIssue("hostname")}
      />
      {mail && (
        <CertCard label={t("panelsslcard.mail")} cert={mail} issuing={issue.isPending} onIssue={() => doIssue("mail")} />
      )}
    </Space>
  );
}

// PanelSSLBand — the SSL Manager's compact SYSTEM band (Certificate
// console). One line for the two panel_certificate rows (hostname +
// mail): status, expiry and a per-kind Issue/retry, plus the Use-LE
// switch. The full card — staging toggle, routable diagnostics, error
// details — stays on Server Settings → Panel SSL; the band links there.
import { Link } from "react-router";
import { Alert, Button, Card, Popconfirm, Space, Switch, Tooltip, Typography } from "antd";
import { ReloadOutlined } from "@icons";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  type PanelCertKind,
  type PanelCertificate,
  usePanelCertificate,
  usePanelCertificateIssue,
  usePanelCertificateToggle,
} from "../../hooks/usePanelCertificate";
import { panelCertExpiryHint, panelCertStatusTag } from "./panelCertStatus";

function BandCert({
  cert,
  onIssue,
  issuing,
}: {
  cert: PanelCertificate;
  onIssue: () => void;
  issuing: boolean;
}) {
  const expiry = panelCertExpiryHint(cert);
  return (
    <Space size={8} wrap>
      <code>{cert.hostname || "<unset>"}</code>
      {panelCertStatusTag(cert)}
      {expiry && (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {expiry}
        </Typography.Text>
      )}
      {!cert.routable && (
        <Tooltip title={cert.routable_reason || "Hostname does not resolve publicly to this server."}>
          <Typography.Text type="warning" style={{ fontSize: 12 }}>
            not routable
          </Typography.Text>
        </Tooltip>
      )}
      <Popconfirm title={`Issue the ${cert.kind} cert now?`} onConfirm={onIssue}>
        <Button size="small" icon={<ReloadOutlined />} loading={issuing}>
          Issue / retry
        </Button>
      </Popconfirm>
    </Space>
  );
}

export function PanelSSLBand() {
  const q = usePanelCertificate();
  const toggle = usePanelCertificateToggle();
  const issue = usePanelCertificateIssue();

  if (q.isPending) {
    return <Card size="small" loading />;
  }
  if (q.isError || !q.data || q.data.length === 0) {
    return (
      <Card size="small">
        <Alert
          type="warning"
          showIcon
          message="Panel certificate state unavailable"
          description={
            <span>
              Manage the panel&apos;s own certs in{" "}
              <Link to="/jabali-admin/settings">Server Settings &rarr; Panel SSL</Link>.
            </span>
          }
        />
      </Card>
    );
  }

  const host = q.data.find((c) => c.kind === "hostname") ?? q.data[0];
  const mail = q.data.find((c) => c.kind === "mail");

  const doIssue = (kind: PanelCertKind) =>
    issue.mutate(kind, {
      onSuccess: () => feedback.message.success(`${kind} cert: issued`),
      onError: (e) =>
        feedback.message.error(`${kind} cert: issue failed: ${String((e as Error).message)}`),
    });

  return (
    <Card size="small" styles={{ body: { padding: "12px 16px" } }}>
      <Space size={16} wrap style={{ width: "100%" }}>
        <Typography.Text strong type="secondary" style={{ fontSize: 13 }}>
          SYSTEM
        </Typography.Text>
        <BandCert cert={host} issuing={issue.isPending} onIssue={() => doIssue("hostname")} />
        {mail && <BandCert cert={mail} issuing={issue.isPending} onIssue={() => doIssue("mail")} />}
        <Space size={8} style={{ marginLeft: "auto" }}>
          <Switch
            size="small"
            checked={host.use_le}
            disabled={!host.routable && !host.use_le}
            loading={toggle.isPending}
            onChange={(v) =>
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
              )
            }
          />
          <Typography.Text style={{ fontSize: 13 }}>Let&apos;s Encrypt</Typography.Text>
          <Link to="/jabali-admin/settings" style={{ fontSize: 13 }}>
            Details
          </Link>
        </Space>
      </Space>
    </Card>
  );
}

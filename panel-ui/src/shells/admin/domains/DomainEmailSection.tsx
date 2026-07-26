// DomainEmailSection — enable/disable email on a domain + show the
// DNS records the operator needs to publish.
//
// Mirrors DomainSSLSection's visual shape (Switch + small warning
// alerts) so the two feel native on DomainEdit. The DKIM key is
// surfaced as a copyable monospace block; MX/SPF/DMARC rows come
// from the API's `records` hint list. Live record-presence status
// is blueprint Step 5 scope (DNS autoconfig) — until then, hints
// render as static instructions (empty `status` column).
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Alert,
  Button,
  Popconfirm,
  Card,
  Select,
  Skeleton,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import { CopyOutlined } from "@icons";

import {
  useDisableDomainEmail,
  useDomainEmail,
  useEnableDomainEmail,
  type DomainEmailDNSHint,
} from "../../../hooks/useMailboxes";
import { useOneQuery } from "../../../hooks/useQueries";
import { useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";

type Props = {
  domainId: string;
};

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    message.success("Copied to clipboard");
  } catch {
    message.error("Copy failed — select the field and copy manually");
  }
}

export const DomainEmailSection = ({ domainId }: Props) => {
  const { t } = useTranslation();
  const { data, isLoading } = useDomainEmail(domainId);
  const enableMutation = useEnableDomainEmail();
  const disableMutation = useDisableDomainEmail();
  const [flipping, setFlipping] = useState(false);
  const qc = useQueryClient();
  const { data: domain } = useOneQuery<{
    webmail_enabled: boolean;
    dmarc_np?: string;
    dmarc_testing?: boolean;
  }>({
    resource: "domains",
    id: domainId,
  });
  const [wmFlipping, setWmFlipping] = useState(false);
  const [rotating, setRotating] = useState(false);

  // Rotate DKIM (Gitea #542): the agent generates a fresh keypair, the panel
  // republishes the DKIM TXT. Remote receivers may need DNS propagation time.
  const onRotateDKIM = async () => {
    setRotating(true);
    try {
      await apiClient.post(`/domains/${domainId}/email/dkim-rotate`);
      qc.invalidateQueries({ queryKey: ["one", "domain-email", domainId] });
      message.success("DKIM rotated — new key published; allow time for DNS propagation");
    } catch (err) {
      const resp = (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data;
      message.error(resp?.detail ?? resp?.error ?? "DKIM rotation failed");
    } finally {
      setRotating(false);
    }
  };
  const onWebmailFlip = async (next: boolean) => {
    setWmFlipping(true);
    try {
      await apiClient.patch(`/domains/${domainId}`, { webmail_enabled: next });
      qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
      message.success(next ? "Webmail enabled for this domain" : "Webmail disabled for this domain");
    } catch {
      message.error("Failed to toggle webmail");
    } finally {
      setWmFlipping(false);
    }
  };

  const [dmarcSaving, setDmarcSaving] = useState(false);
  const saveDmarc = async (patch: { dmarc_np?: string; dmarc_testing?: boolean }) => {
    setDmarcSaving(true);
    try {
      await apiClient.patch(`/domains/${domainId}`, patch);
      qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
      message.success("DMARC settings saved");
    } catch (err) {
      const resp = (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data;
      message.error(resp?.detail ?? resp?.error ?? "Failed to save DMARC settings");
    } finally {
      setDmarcSaving(false);
    }
  };

  const onFlip = async (next: boolean) => {
    setFlipping(true);
    try {
      if (next) {
        await enableMutation.mutateAsync({ domainId });
        message.success("Email enabled — publish the DNS records below");
      } else {
        await disableMutation.mutateAsync({ domainId });
        message.success("Email disabled");
      }
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })
        ?.response?.data;
      message.error(resp?.detail ?? resp?.error ?? "Failed to toggle email");
    } finally {
      setFlipping(false);
    }
  };

  if (isLoading && !data) {
    return <Skeleton active paragraph={{ rows: 3 }} />;
  }
  if (!data) {
    return <Alert type="error" showIcon title={t("domainemailsection.failed_to_load_email_state")} />;
  }

  const enabled = data.email_enabled;
  const dkim = data.dkim_public_key ?? "";

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="large">
      <Space size="middle" align="center" wrap>
        <Switch checked={enabled} loading={flipping} onChange={onFlip} />
        <span>
          Incoming + outgoing mail for{" "}
          <Typography.Text code>{data.domain_name}</Typography.Text>
        </span>
      </Space>

      {enabled && (
        <Space size="middle" align="center" wrap>
          <Switch
            checked={domain?.webmail_enabled ?? true}
            loading={wmFlipping}
            onChange={onWebmailFlip}
          />
          <span>
            Webmail client (Bulwark) for this domain — turn off to drop just the
            <Typography.Text code> mail.{data.domain_name}</Typography.Text> webmail
            vhost; mail delivery is unaffected.
          </span>
        </Space>
      )}

      {enabled && (
        <Card size="small" title="DMARC (DMARCbis)">
          <Space direction="vertical" size="middle" style={{ width: "100%" }}>
            <Space align="center" wrap>
              <span>Non-existent subdomain policy (np):</span>
              <Select
                style={{ minWidth: 240 }}
                loading={dmarcSaving}
                value={domain?.dmarc_np ?? ""}
                onChange={(v) => void saveDmarc({ dmarc_np: v })}
                options={[
                  { value: "", label: "Inherit subdomain policy (sp)" },
                  { value: "none", label: "none — monitor only" },
                  { value: "quarantine", label: "quarantine" },
                  { value: "reject", label: "reject — strictest" },
                ]}
              />
            </Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              DMARCbis <Typography.Text code>np</Typography.Text> sets the policy for mail from{" "}
              <em>non-existent</em> subdomains (phantom-subdomain protection). Empty = receivers fall
              back to the subdomain policy (sp).
            </Typography.Text>
            <Space align="center" wrap>
              <Switch
                checked={domain?.dmarc_testing ?? false}
                loading={dmarcSaving}
                onChange={(v) => void saveDmarc({ dmarc_testing: v })}
              />
              <span>
                Testing mode (<Typography.Text code>t=y</Typography.Text>) — receivers evaluate the
                policy but treat it as monitoring.
              </span>
            </Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Applies to the panel-managed <Typography.Text code>_dmarc</Typography.Text> record; a
              hand-edited DMARC record is left untouched.
            </Typography.Text>
          </Space>
        </Card>
      )}

      {enabled && !dkim && (
        // Paranoid guard: the panel is in an inconsistent state (enabled
        // but no DKIM material). Surface so the operator can disable +
        // re-enable rather than silently ship broken outbound mail.
        <Alert
          type="error"
          showIcon
          title={t("domainemailsection.dkim_key_missing")}
          description={t("domainemailsection.email_is_enabled_but_no_dkim_public_key_is_s")}
        />
      )}

      {enabled && dkim && (
        <Popconfirm
          title={t("domainemailsection.rotate_dkim_key")}
          description={t("domainemailsection.a_fresh_dkim_keypair_is_generated_and_the_dn")}
          okText={t("domainemailsection.rotate")}
          okButtonProps={{ danger: true }}
          onConfirm={onRotateDKIM}
        >
          <Button danger loading={rotating}>Rotate DKIM</Button>
        </Popconfirm>
      )}

      {enabled && data.warnings && data.warnings.length > 0 && (
        // Panel-API sends these when an M6 record can't be written
        // because a user-edited row blocks the slot. One line per
        // blocked record — no header, no dismissal: the operator
        // needs to resolve them by touching DNS.
        <Alert
          type="warning"
          showIcon
          message={t("domainemailsection.dns_autoconfig_partially_applied")}
          description={
            <ul style={{ margin: 0, paddingInlineStart: 20 }}>
              {data.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          }
        />
      )}

      {enabled && (
        <Card size="small" title={t("domainemailsection.dns_records")}>
          <Table<DomainEmailDNSHint>
            size="small"
            pagination={false}
            dataSource={data.records}
            scroll={{ x: "max-content" }}
            rowKey={(r) => `${r.type}:${r.name}`}
            columns={[
              {
                title: "Status",
                dataIndex: "status",
                width: 100,
                render: (status: string | undefined) => renderStatusTag(status),
              },
              {
                title: "Purpose",
                dataIndex: "purpose",
                width: 280,
                render: (text: string) => (
                  <Typography.Text type="secondary">{text}</Typography.Text>
                ),
              },
              { title: "Name", dataIndex: "name", width: 260 },
              { title: "Type", dataIndex: "type", width: 60 },
              {
                title: "Value",
                dataIndex: "value",
                render: (value: string) =>
                  value ? (
                    <Space size="small">
                      <Typography.Text
                        code
                        copyable={false}
                        style={{
                          wordBreak: "break-all",
                          fontSize: 12,
                        }}
                      >
                        {value}
                      </Typography.Text>
                      <Button
                        size="small"
                        icon={<CopyOutlined />}
                        onClick={() => copyText(value)}
                        aria-label={t("domainemailsection.copy_value")}
                      />
                    </Space>
                  ) : (
                    <Typography.Text type="secondary" italic>
                      generated on enable
                    </Typography.Text>
                  ),
              },
            ]}
          />
        </Card>
      )}
    </Space>
  );
};

// renderStatusTag maps the panel-API's 4 documented status values to
// AntD tag colours. Empty string is "no live data" — the panel has no
// zone on file, usually because PowerDNS isn't wired in this install.
// "ok" is green (present + managed or matching), "missing" is default
// (zone has no row there), "conflict" is red (user-edited row blocks M6).
function renderStatusTag(status: string | undefined) {
  switch (status) {
    case "ok":
      return <Tag color="green">ok</Tag>;
    case "missing":
      return <Tag color="default">missing</Tag>;
    case "conflict":
      return <Tag color="red">conflict</Tag>;
    default:
      return <Tag color="default">—</Tag>;
  }
}

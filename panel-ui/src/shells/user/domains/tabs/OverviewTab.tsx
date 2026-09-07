// OverviewTab — the landing pane of the tenant Web Domain page (GH #1543).
// Shows the domain's key facts and the two instant per-domain toggles that
// were previously buried in the row's "Actions" menu (preview URL, bot
// challenge). Both PATCH /domains/:id and invalidate the single-row + list
// caches so the badge and the list stay in step, mirroring the DomainInventory
// row handlers.
import { useState } from "react";
import { Button, Descriptions, Space, Switch, Tag, Typography } from "antd";
import { SafetyCertificateOutlined } from "@icons";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { feedback } from "../../../../lib/feedback";
import { getSSLTag } from "../../../../utils/sslState";
import { daysUntil } from "../../../../components/ssl/sslHealth";
import { SSLCertViewModal } from "../../../../components/ssl/SSLCertViewModal";
import type { Domain } from "../../../../components/domains/types";

const stripHomePrefix = (path: string): string => {
  if (path.startsWith("/home/")) {
    const match = path.match(/^\/home\/[^/]+\/(.*)/);
    return match ? match[1] : path;
  }
  return path;
};

export const OverviewTab = ({ domain }: { domain: Domain }) => {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [busy, setBusy] = useState<null | "preview" | "bot">(null);
  const [viewCert, setViewCert] = useState(false);

  const patch = async (
    field: "preview" | "bot",
    body: Record<string, unknown>,
    success: string,
    errorPrefix: string,
  ) => {
    setBusy(field);
    try {
      await apiClient.patch(`/domains/${domain.id}`, body);
      feedback.message.success(success);
      qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        `${errorPrefix}: ${e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message}`,
      );
      // A long agent call can apply the change and still return 5xx; refetch so
      // the toggle reflects the server, not the optimistic local value.
      qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
    } finally {
      setBusy(null);
    }
  };

  const togglePreview = (next: boolean) =>
    patch(
      "preview",
      { temp_url_enabled: next },
      next ? "Preview URL enabled — live within a minute" : "Preview URL disabled",
      "Failed to toggle preview URL",
    );

  const toggleBot = (next: boolean) =>
    patch(
      "bot",
      { bot_challenge_include: next },
      next
        ? "Bot-detection challenge enabled — active within a minute if the server is in Selected-domains mode"
        : "Bot-detection challenge disabled for this site",
      "Failed to toggle bot challenge",
    );

  const ssl = getSSLTag(domain.ssl_state);
  // An on-disk certificate to view/date exists only once issued — for a parked
  // (pending_acme_retry) cert the nested expires_at is the self-signed
  // fallback's date, so never show it as the cert's expiry (GH #1543).
  const sslIssued = domain.ssl?.status === "issued";
  const sslExpiryDays = sslIssued ? daysUntil(domain.ssl?.expires_at ?? null) : null;
  const goToSSL = () => navigate(`/jabali-panel/domains/${domain.id}/ssl`);

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Descriptions column={1} size="small" bordered>
        <Descriptions.Item label="Status">
          {domain.is_enabled ? <Tag color="green">Enabled</Tag> : <Tag color="red">Disabled</Tag>}
        </Descriptions.Item>
        <Descriptions.Item label="SSL">
          <Space wrap size="small">
            <Tag color={ssl.color}>{ssl.label}</Tag>
            {sslIssued && sslExpiryDays !== null ? (
              <Typography.Text type="secondary" style={{ fontSize: 13 }}>
                expires in {sslExpiryDays} day{sslExpiryDays === 1 ? "" : "s"}
              </Typography.Text>
            ) : null}
            {sslIssued ? (
              <Button size="small" icon={<SafetyCertificateOutlined />} onClick={() => setViewCert(true)}>
                View Certificate
              </Button>
            ) : null}
            <Button size="small" type="link" style={{ padding: 0 }} onClick={goToSSL}>
              SSL settings
            </Button>
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="Document root">
          <Typography.Text code>{stripHomePrefix(domain.doc_root)}</Typography.Text>
        </Descriptions.Item>
        {domain.reverse_proxy_port ? (
          <Descriptions.Item label="Reverse proxy">
            <Typography.Text>Port {domain.reverse_proxy_port}</Typography.Text>
          </Descriptions.Item>
        ) : null}
        {domain.temp_url_enabled && domain.temp_url ? (
          <Descriptions.Item label="Preview URL">
            <Typography.Link href={domain.temp_url} target="_blank" rel="noopener noreferrer">
              {domain.temp_url}
            </Typography.Link>
          </Descriptions.Item>
        ) : null}
      </Descriptions>

      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Space align="center">
          <Switch
            checked={!!domain.temp_url_enabled}
            loading={busy === "preview"}
            onChange={togglePreview}
            aria-label="Preview URL"
          />
          <div>
            <Typography.Text strong>Preview URL</Typography.Text>
            <Typography.Paragraph type="secondary" style={{ margin: 0, fontSize: 13 }}>
              A temporary hostname to preview the site before DNS points at the server.
            </Typography.Paragraph>
          </div>
        </Space>

        <Space align="center">
          <Switch
            checked={!!domain.bot_challenge_include}
            loading={busy === "bot"}
            onChange={toggleBot}
            aria-label="Bot-detection challenge"
          />
          <div>
            <Typography.Text strong>Bot-detection challenge</Typography.Text>
            <Typography.Paragraph type="secondary" style={{ margin: 0, fontSize: 13 }}>
              Challenge suspicious visitors when the server runs in Selected-domains mode.
            </Typography.Paragraph>
          </div>
        </Space>
      </Space>

      <SSLCertViewModal
        domainId={viewCert ? domain.id : null}
        domainName={domain.name}
        onClose={() => setViewCert(false)}
      />
    </Space>
  );
};

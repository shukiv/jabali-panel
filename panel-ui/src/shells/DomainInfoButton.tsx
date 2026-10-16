// Domain Information modal (GH #254) — shows panel-side domain facts plus a
// live WHOIS lookup (registrar, key dates, name servers, status) with the raw
// record available in a collapse. WHOIS is a network call to the registry, so
// it loads lazily when the modal opens and degrades gracefully: an unknown
// TLD format still shows the raw text, and a lookup failure shows the panel
// facts with an inline error.
import { useEffect, useState } from "react";
import {
  Alert,
  Collapse,
  Descriptions,
  Modal,
  Spin,
  Tag,
  Typography,
} from "antd";

import { apiClient } from "../apiClient";
import { shortDateTime } from "../utils/datetime";

export type DomainInfoTarget = {
  id: string;
  name: string;
  username?: string | null;
  doc_root?: string;
  is_enabled?: boolean;
  ssl_enabled?: boolean;
  listen_ipv4?: { id: number; address: string } | null;
  listen_ipv6?: { id: number; address: string } | null;
  created_at?: string;
};

type WhoisData = {
  domain: string;
  registrar?: string;
  created_date?: string;
  updated_date?: string;
  expiry_date?: string;
  registrant?: string;
  name_servers?: string[];
  statuses?: string[];
  raw: string;
};

type WhoisResponse = {
  domain_id: string;
  domain_name: string;
  whois: WhoisData;
};

const fmtDate = (s?: string) => {
  if (!s) return "—";
  return isNaN(new Date(s).getTime()) ? s : shortDateTime(s);
};

export const DomainInfoButton = ({
  domain,
  open,
  onClose,
}: {
  domain: DomainInfoTarget;
  open: boolean;
  onClose: () => void;
}) => {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [whois, setWhois] = useState<WhoisData | null>(null);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    setWhois(null);
    apiClient
      // 25s axios ceiling > the 20s server WHOIS deadline so the registry
      // call isn't cut short by the client's default 15s timeout.
      .get<WhoisResponse>(`/domains/${domain.id}/whois`, { timeout: 25000 })
      .then((res) => {
        if (!cancelled) setWhois(res.data.whois);
      })
      .catch((e) => {
        if (!cancelled)
          setError(
            e?.response?.data?.details ||
              e?.response?.data?.error ||
              "WHOIS lookup failed",
          );
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, domain.id]);

  const ip =
    [domain.listen_ipv4?.address, domain.listen_ipv6?.address]
      .filter(Boolean)
      .join(", ") || "Server default";

  return (
    <Modal
      open={open}
      onCancel={onClose}
      onOk={onClose}
      title={`Domain Information — ${domain.name}`}
      width={720}
      footer={null}
    >
      <Descriptions
        size="small"
        column={1}
        bordered
        styles={{ label: { width: 160 } }}
      >
        <Descriptions.Item label="Domain">{domain.name}</Descriptions.Item>
        {domain.username && (
          <Descriptions.Item label="Owner">{domain.username}</Descriptions.Item>
        )}
        {domain.doc_root && (
          <Descriptions.Item label="Document Root">
            <Typography.Text code>{domain.doc_root}</Typography.Text>
          </Descriptions.Item>
        )}
        <Descriptions.Item label="Listen IP">{ip}</Descriptions.Item>
        <Descriptions.Item label="Status">
          {domain.is_enabled === false ? (
            <Tag color="default">Disabled</Tag>
          ) : (
            <Tag color="green">Enabled</Tag>
          )}
          {domain.ssl_enabled ? <Tag color="blue">SSL</Tag> : null}
        </Descriptions.Item>
        {domain.created_at && (
          <Descriptions.Item label="Created">
            {fmtDate(domain.created_at)}
          </Descriptions.Item>
        )}
      </Descriptions>

      <Typography.Title level={5} style={{ marginTop: 20 }}>
        WHOIS
      </Typography.Title>

      {loading && (
        <div style={{ textAlign: "center", padding: 24 }}>
          <Spin tip="Looking up registry…" />
        </div>
      )}

      {!loading && error && (
        <Alert type="warning" showIcon message="WHOIS unavailable" description={error} />
      )}

      {!loading && whois && (
        <>
          <Descriptions
            size="small"
            column={1}
            bordered
            styles={{ label: { width: 160 } }}
          >
            <Descriptions.Item label="Registrar">
              {whois.registrar || "—"}
            </Descriptions.Item>
            <Descriptions.Item label="Registrant">
              {whois.registrant || "—"}
            </Descriptions.Item>
            <Descriptions.Item label="Created">
              {fmtDate(whois.created_date)}
            </Descriptions.Item>
            <Descriptions.Item label="Updated">
              {fmtDate(whois.updated_date)}
            </Descriptions.Item>
            <Descriptions.Item label="Expires">
              {fmtDate(whois.expiry_date)}
            </Descriptions.Item>
            <Descriptions.Item label="Name Servers">
              {whois.name_servers?.length
                ? whois.name_servers.map((ns) => <div key={ns}>{ns}</div>)
                : "—"}
            </Descriptions.Item>
            <Descriptions.Item label="Status">
              {whois.statuses?.length
                ? whois.statuses.map((s) => (
                    <div key={s}>
                      <Typography.Text type="secondary">{s}</Typography.Text>
                    </div>
                  ))
                : "—"}
            </Descriptions.Item>
          </Descriptions>

          <Collapse
            style={{ marginTop: 12 }}
            items={[
              {
                key: "raw",
                label: "Raw WHOIS record",
                children: (
                  <Typography.Paragraph>
                    <pre
                      style={{
                        maxHeight: 320,
                        overflow: "auto",
                        whiteSpace: "pre-wrap",
                        wordBreak: "break-word",
                        margin: 0,
                        fontSize: 12,
                      }}
                    >
                      {whois.raw}
                    </pre>
                  </Typography.Paragraph>
                ),
              },
            ]}
          />
        </>
      )}
    </Modal>
  );
};

// DomainDeliverabilitySection — M47 Wave 9b per-domain deliverability
// widget. Reuses the admin-wide /admin/mail/deliverability endpoint
// with ?domain=<name> to narrow DMARC + TLS-RPT counts to one domain.
// RBL stays server-wide (the IP is shared across all hosted domains)
// so it's omitted from the per-domain view; the operator sees it on
// the server-wide /jabali-admin/mail/deliverability page.
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { Alert, Card, Descriptions, Progress, Skeleton, Tag, Typography } from "antd";

import { apiClient } from "../../../apiClient";

type Component = {
  name: string;
  value: number;
  deduction: number;
  detail: string;
};

type ScoreResponse = {
  score: number;
  severity: "ok" | "warning" | "critical";
  generated_at: string;
  domain: string;
  components: Component[];
};

const severityColour: Record<ScoreResponse["severity"], string> = {
  ok: "green",
  warning: "gold",
  critical: "red",
};

type Props = { domainName: string };

export const DomainDeliverabilitySection = ({ domainName }: Props) => {
  const { t } = useTranslation();
  const { data, isLoading, error } = useQuery({
    queryKey: ["admin", "mail", "deliverability", domainName],
    queryFn: async () =>
      (await apiClient.get<ScoreResponse>(`/admin/mail/deliverability?domain=${encodeURIComponent(domainName)}`)).data,
    refetchInterval: 60_000,
    enabled: !!domainName,
  });

  if (isLoading) return <Skeleton active />;
  if (error || !data) {
    return <Alert type="warning" message={t("domaindeliverabilitysection.deliverability_score_unavailable_for_this_do")} />;
  }
  // Filter out the rbl component server-side returns even with ?domain= set
  // (RBL is server-wide; the handler already skips it when domain is set,
  // so this is defensive).
  const components = data.components.filter((c) => c.name !== "rbl");

  return (
    <Card title={t("domaindeliverabilitysection.deliverability_score")} size="small">
      <div style={{ marginBottom: 16, display: "flex", alignItems: "center", gap: 16 }}>
        <Progress
          type="circle"
          percent={data.score}
          strokeColor={severityColour[data.severity]}
          format={(p) => `${p}/100`}
          width={100}
        />
        <div>
          <Tag color={severityColour[data.severity]}>{data.severity.toUpperCase()}</Tag>
          <div>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {data.domain} · last 7d
            </Typography.Text>
          </div>
        </div>
      </div>
      <Descriptions bordered column={1} size="small">
        {components.map((c) => (
          <Descriptions.Item
            key={c.name}
            label={
              <span>
                {c.name}{" "}
                <Tag color={c.deduction === 0 ? "green" : c.deduction >= 20 ? "red" : "gold"}>
                  -{c.deduction}
                </Tag>
              </span>
            }
          >
            <strong>{c.value}</strong>{" "}
            <Typography.Text type="secondary">{c.detail}</Typography.Text>
          </Descriptions.Item>
        ))}
      </Descriptions>
      <Typography.Text type="secondary" style={{ fontSize: 11, display: "block", marginTop: 8 }}>
        Server-wide RBL listings are tracked separately at{" "}
        <a href="/jabali-admin/mail/deliverability">/jabali-admin/mail/deliverability</a>.
      </Typography.Text>
    </Card>
  );
};

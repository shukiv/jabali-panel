// StatisticsTab — GH #873 round 4: tenant-scoped mail traffic. Shows the
// per-domain send/receive breakdown for ONLY the caller's own domains
// (GET /mail/stats is scoped server-side to the authenticated user). Read-only.
import { useState } from "react";
import { Alert, Card, Radio, Table, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";

type DomainTraffic = {
  domain: string;
  sent: number;
  received: number;
  delivered: number;
  failed: number;
};

type TenantStats = { traffic: DomainTraffic[] };

const rangeLabel = (hours: number) =>
  hours >= 168 ? `${hours / 24}d` : `${hours}h`;

// GH #1387: domainName filters the per-domain series to one domain (drill-down)
// and hides the Domain column; unset = the full cross-domain breakdown.
export const StatisticsTab = ({ domainName }: { domainName?: string } = {}) => {
  const [hours, setHours] = useState(24);
  const stats = useQuery<TenantStats>({
    queryKey: ["mail", "stats", hours],
    queryFn: async () =>
      (await apiClient.get<TenantStats>(`/mail/stats?hours=${hours}`)).data,
    refetchInterval: 60_000,
  });

  if (stats.isError) {
    return <Alert type="error" message="Failed to load mail statistics" />;
  }

  const traffic = (stats.data?.traffic ?? []).filter(
    (r) => !domainName || r.domain === domainName,
  );

  return (
    <>
      <Radio.Group
        value={hours}
        onChange={(e) => setHours(e.target.value)}
        style={{ marginBottom: 16 }}
        options={[
          { label: "24h", value: 24 },
          { label: "7d", value: 168 },
          { label: "30d", value: 720 },
        ]}
        optionType="button"
      />

      <Card
        size="small"
        title={`Traffic by domain (${rangeLabel(hours)})`}
        loading={stats.isLoading}
      >
        {traffic.length === 0 ? (
          <Typography.Text type="secondary">
            Collecting — your per-domain mail counts appear here once mail flows.
          </Typography.Text>
        ) : (
          <>
            <Typography.Paragraph
              type="secondary"
              style={{ marginTop: 0, fontSize: 12 }}
            >
              Counted from the mail delivery log by sender/recipient domain.
            </Typography.Paragraph>
            <Table
              size="small"
              rowKey="domain"
              pagination={false}
              dataSource={traffic}
              columns={[
                ...(domainName
                  ? []
                  : [{ title: "Domain", dataIndex: "domain" as const }]),
                { title: "Sent", dataIndex: "sent", width: 90, align: "right" },
                { title: "Received", dataIndex: "received", width: 100, align: "right" },
                { title: "Delivered", dataIndex: "delivered", width: 100, align: "right" },
                { title: "Failed", dataIndex: "failed", width: 90, align: "right" },
              ]}
            />
          </>
        )}
      </Card>
    </>
  );
};

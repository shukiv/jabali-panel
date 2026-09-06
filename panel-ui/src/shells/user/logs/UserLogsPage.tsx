// UserLogsPage — the tenant's Logs & Statistics: per-domain web log streams
// plus account activity. The stream lifecycle, request shaping, and columns
// are the neutral Domain Log Streams module (JAB-296); this shell adds the
// tenant deltas: the outer Domain-logs / Account-activity tabs, the ?domain=
// focus filter (GH #1332), and — by using the domain-only column context — the
// structural guarantee that no request here ever omits domain identity (AC2).
import { Card, Typography, Button, Space, Table, Tabs } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { LogStreamModal } from "../../../components/LogStreamModal";
import { AccountActivity } from "../activity/AccountActivity";
import { useSearchParams } from "react-router";
import { useState } from "react";
import { type DomainLogRow } from "../../../components/logs/domainLogStreams";
import { useDomainLogStreams } from "../../../components/logs/useDomainLogStreams";
import { buildDomainLogColumns } from "../../../components/logs/buildDomainLogColumns";

const { Title } = Typography;

export const UserLogsPage = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = searchParams.get("tab") === "activity" ? "activity" : "domains";
  const [refreshTrigger, setRefreshTrigger] = useState(0);
  const streams = useDomainLogStreams();

  const { data: domainsData, isLoading } = useQuery({
    queryKey: ["user-domains", refreshTrigger],
    queryFn: async () => {
      const response = await apiClient.get("/domains");
      return response.data;
    },
  });

  const domains: DomainLogRow[] = domainsData?.data || [];

  // GH #1332 item 7: the per-domain PHP Settings page links here with
  // ?domain=<id> to jump straight to that site's logs. Filter to it (and offer
  // a one-click "show all") when present; unknown/foreign ids just fall through
  // to the full list since the table is already scoped to the caller's domains.
  const focusDomainId = searchParams.get("domain");
  const shownDomains =
    focusDomainId && domains.some((d) => d.id === focusDomainId)
      ? domains.filter((d) => d.id === focusDomainId)
      : domains;
  const isFilteredToDomain =
    !!focusDomainId && shownDomains.length < domains.length;

  // Domain-only context: no onOpenAggregate, so the tenant scope cannot reach
  // an aggregate request — every open carries this domain's id.
  const columns = buildDomainLogColumns({ onOpenDomain: streams.openStream });

  const domainLogsTab = (
    <>
      <Space style={{ marginBottom: 16, width: "100%", justifyContent: "flex-end" }}>
        {isFilteredToDomain && (
          <Button onClick={() => setSearchParams({}, { replace: true })}>
            Show all domains
          </Button>
        )}
        <Button
          type="primary"
          icon={<ReloadOutlined />}
          onClick={() => setRefreshTrigger((p) => p + 1)}
        >
          Refresh
        </Button>
      </Space>
      <Card>
        <Table
          columns={columns}
          dataSource={shownDomains}
          rowKey="id"
          loading={isLoading}
          pagination={false}
          size="middle"
          scroll={{ x: "max-content" }}
        />
      </Card>
    </>
  );

  return (
    <div>
      <Title level={2} style={{ marginTop: 0 }}>
        Logs & Statistics
      </Title>

      <Tabs
        activeKey={activeTab}
        onChange={(k) => setSearchParams(k === "activity" ? { tab: "activity" } : {}, { replace: true })}
        items={[
          { key: "domains", label: "Domain logs", children: domainLogsTab },
          { key: "activity", label: "Account activity", children: <AccountActivity /> },
        ]}
      />

      <LogStreamModal {...streams.modalProps} />
    </div>
  );
};

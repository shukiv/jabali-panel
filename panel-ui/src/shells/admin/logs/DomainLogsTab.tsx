// DomainLogsTab — per-domain web log streams (access / error / GoAccess
// real-time) for the admin. One tab of the consolidated Logs & Statistics
// page. The stream lifecycle, request shaping, and columns are the neutral
// Domain Log Streams module (JAB-296); this shell only adds the admin delta:
// the "All Domains" aggregate row and its cross-domain open capability.
import { useState } from "react";
import { Card, Button, Space, Table } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { LogStreamModal } from "../../../components/LogStreamModal";
import {
  type DomainLogRow,
  ALL_DOMAINS_ROW,
} from "../../../components/logs/domainLogStreams";
import { useDomainLogStreams } from "../../../components/logs/useDomainLogStreams";
import { buildDomainLogColumns } from "../../../components/logs/buildDomainLogColumns";

export const DomainLogsTab = () => {
  const [refreshTrigger, setRefreshTrigger] = useState(0);
  const streams = useDomainLogStreams();

  const { data: domainsData, isLoading } = useQuery({
    queryKey: ["domains", refreshTrigger],
    queryFn: async () => {
      const response = await apiClient.get("/domains");
      return response.data;
    },
  });

  const domains: DomainLogRow[] = domainsData?.data || [];

  // Admin delta: the synthetic aggregate row, and the capability to open a
  // cross-domain stream on it. onOpenAggregate is what makes the aggregate
  // reachable here (and, by its absence, unreachable in the tenant scope).
  const tableData: DomainLogRow[] = [ALL_DOMAINS_ROW, ...domains];
  const columns = buildDomainLogColumns({
    onOpenDomain: streams.openStream,
    onOpenAggregate: (logType) => streams.openStream(logType),
  });

  return (
    <div>
      <Space style={{ marginBottom: 16, width: "100%", justifyContent: "flex-end" }}>
        <Button
          icon={<ReloadOutlined />}
          onClick={() => setRefreshTrigger((p) => p + 1)}
        >
          Refresh
        </Button>
      </Space>

      <Card>
        <Table
          columns={columns}
          dataSource={tableData}
          rowKey="id"
          loading={isLoading}
          pagination={false}
          size="middle"
          scroll={{ x: "max-content" }}
        />
      </Card>

      <LogStreamModal {...streams.modalProps} />
    </div>
  );
};

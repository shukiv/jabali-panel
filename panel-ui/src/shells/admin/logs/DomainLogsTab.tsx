// DomainLogsTab — per-domain web log streams (access / error / GoAccess
// real-time) for the admin. One tab of the consolidated Logs & Statistics
// page. The stream lifecycle, request shaping, and columns are the neutral
// Domain Log Streams module (JAB-296); this shell only adds the admin delta:
// the "All Domains" aggregate row and its cross-domain open capability. The
// page's card frames the tab, so the table sits in it directly (GH #2251).
import { useState } from "react";
import { Button, Space } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { fetchAllPages } from "../../../lib/fetchAllPages";
import { LogStreamModal } from "../../../components/LogStreamModal";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import {
  type DomainLogRow,
  ALL_DOMAINS_ROW,
  DOMAIN_LOG_PAGINATION,
  matchesDomainSearch,
} from "../../../components/logs/domainLogStreams";
import { useDomainLogStreams } from "../../../components/logs/useDomainLogStreams";
import { buildDomainLogColumns } from "../../../components/logs/buildDomainLogColumns";

export const DomainLogsTab = () => {
  const [refreshTrigger, setRefreshTrigger] = useState(0);
  const [search, setSearch] = useState("");
  const streams = useDomainLogStreams();

  // Every page: asked for no page size, /domains answers its first 20 rows,
  // and the rest never showed here (GH #2381).
  const { data: domains = [], isLoading } = useQuery({
    queryKey: ["domains", "all", refreshTrigger],
    queryFn: () => fetchAllPages<DomainLogRow>("/domains"),
  });

  // Admin delta: the synthetic aggregate row, and the capability to open a
  // cross-domain stream on it. onOpenAggregate is what makes the aggregate
  // reachable here (and, by its absence, unreachable in the tenant scope).
  // The search narrows the domains only; the aggregate row stays on top.
  const tableData: DomainLogRow[] = [
    ALL_DOMAINS_ROW,
    ...domains.filter((d) => matchesDomainSearch(d, search)),
  ];
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

      <SearchableTableStringQ<DomainLogRow>
        columns={columns}
        dataSource={tableData}
        rowKey="id"
        loading={isLoading}
        searchPlaceholder="Search domains…"
        onSearchChange={setSearch}
        pagination={DOMAIN_LOG_PAGINATION}
        size="middle"
        scroll={{ x: "max-content" }}
      />

      <LogStreamModal {...streams.modalProps} />
    </div>
  );
};

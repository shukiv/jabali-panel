// UserLogsPage — the tenant's Logs & Statistics: per-domain web log streams
// plus account activity. The stream lifecycle, request shaping, and columns
// are the neutral Domain Log Streams module (JAB-296); this shell adds the
// tenant deltas: the outer Domain-logs / Account-activity tabs, the ?domain=
// focus filter (GH #1332), and — by using the domain-only column context — the
// structural guarantee that no request here ever omits domain identity (AC2).
// Laid out like Security and Backups (GH #2251): the heading with the
// sidebar's icon, then one card whose tab bar holds the two tabs.
import { Card, Typography, Button, Space } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { FileTextOutlined, GlobalOutlined, UserOutlined } from "@icons";
import { useQuery } from "@tanstack/react-query";
import { fetchAllPages } from "../../../lib/fetchAllPages";
import { LogStreamModal } from "../../../components/LogStreamModal";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { AccountActivity } from "../activity/AccountActivity";
import { useSearchParams } from "react-router";
import { useState } from "react";
import {
  type DomainLogRow,
  DOMAIN_LOG_PAGINATION,
  matchesDomainSearch,
} from "../../../components/logs/domainLogStreams";
import { useDomainLogStreams } from "../../../components/logs/useDomainLogStreams";
import { buildDomainLogColumns } from "../../../components/logs/buildDomainLogColumns";

const { Title } = Typography;

export const UserLogsPage = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = searchParams.get("tab") === "activity" ? "activity" : "domains";
  const [refreshTrigger, setRefreshTrigger] = useState(0);
  const streams = useDomainLogStreams();

  const [search, setSearch] = useState("");

  // Every page: asked for no page size, /domains answers its first 20 rows,
  // and a ?domain= link to a later one fell back to the wrong list (GH #2381).
  const { data: domains = [], isLoading } = useQuery({
    queryKey: ["user-domains", "all", refreshTrigger],
    queryFn: () => fetchAllPages<DomainLogRow>("/domains"),
  });

  // GH #1332 item 7: ?domain=<id> jumps straight to that site's logs (PHP
  // Settings linked here until GH #1701 opened the error log in place; old
  // links keep working). Filter to it (and offer a one-click "show all") when
  // present; unknown/foreign ids just fall through to the full list since the
  // table is already scoped to the caller's domains.
  const focusDomainId = searchParams.get("domain");
  const focusedDomains =
    focusDomainId && domains.some((d) => d.id === focusDomainId)
      ? domains.filter((d) => d.id === focusDomainId)
      : domains;
  const isFilteredToDomain =
    !!focusDomainId && focusedDomains.length < domains.length;
  const shownDomains = focusedDomains.filter((d) => matchesDomainSearch(d, search));

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
      <SearchableTableStringQ<DomainLogRow>
        columns={columns}
        dataSource={shownDomains}
        rowKey="id"
        loading={isLoading}
        searchPlaceholder="Search domains…"
        onSearchChange={setSearch}
        pagination={DOMAIN_LOG_PAGINATION}
        size="middle"
        scroll={{ x: "max-content" }}
      />
    </>
  );

  return (
    <div>
      <Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <FileTextOutlined /> Logs & Statistics
      </Title>

      <Card
        // nowrap keeps two-word tabs on one line at phone width (as Notifications).
        tabList={[
          {
            key: "domains",
            tab: (
              <Space size={6} style={{ whiteSpace: "nowrap" }}>
                <GlobalOutlined />
                Domain logs
              </Space>
            ),
          },
          {
            key: "activity",
            tab: (
              <Space size={6} style={{ whiteSpace: "nowrap" }}>
                <UserOutlined />
                Account activity
              </Space>
            ),
          },
        ]}
        activeTabKey={activeTab}
        onTabChange={(k) => setSearchParams(k === "activity" ? { tab: "activity" } : {}, { replace: true })}
      >
        {activeTab === "activity" ? <AccountActivity /> : domainLogsTab}
      </Card>

      <LogStreamModal {...streams.modalProps} />
    </div>
  );
};

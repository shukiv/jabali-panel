// DNSZonesOverviewPage — admin landing for DNS. Card.tabList splits
// into "Zones" (per-domain provisioning, the batched /dns/zones inventory)
// and "DNSSEC" (signing state + DS export). Mirrors the UserList tab style —
// controlled activeTabKey, panel-attached strip.
import { useTranslation } from "react-i18next";
import { useTabParam } from "../../../hooks/useTabParam";
import { Alert, Button, Card, Spin, Table, Tag, Tooltip, Typography } from "antd";
import { ServerOutlined } from "@icons";
import { useNavigate } from "react-router";

import { columnSearchProps } from "../../../components/columnSearch";
import { DNSSECTable } from "../../../components/dnssec/DNSSECTable";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useTableURL } from "../../../hooks/useTableURL";
import { sorterToParams } from "../../../utils/tableSorter";

// DnsZoneRow is one row of the batched GET /dns/zones inventory (JAB-377):
// the domain plus its provisioning state, record count, and effective TTL,
// resolved server-side in one request instead of a per-row zone fan-out.
interface DnsZoneRow {
  id: string;
  user_id: string;
  username?: string | null;
  name: string;
  provisioned: boolean;
  record_count: number;
  effective_ttl?: number | null;
  dnssec_enabled?: boolean;
  registrar_expires_at?: string | null;
}

const ZonesTab = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();

  // One batched request (JAB-377): the endpoint returns provisioning state +
  // record count + effective TTL per row, so there is no per-domain zone fetch
  // and a transient failure surfaces as an error, not a false "Not provisioned".
  const query = useTableURL<DnsZoneRow>({
    resource: "dns/zones",
    defaultSort: "name",
    defaultOrder: "asc",
  });

  // Project AntD's sorter into the URL params so the server does the
  // ORDER BY. Without an onChange the sort arrows rendered but changed
  // nothing — the columns declared a server-side sorter and nothing was
  // listening for it.
  const handleTableChange: React.ComponentProps<typeof Table<DnsZoneRow>>["onChange"] = (
    _pagination,
    _filters,
    sorter,
  ) => {
    const { sort, order } = sorterToParams<DnsZoneRow>(sorter);
    query.setParams({ sort, order, page: 1 });
  };

  return (
    <>
      <Alert
        title={t("dnszonesoverviewpage.dns_zones_are_provisioned_automatically_when")}
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
      />
      {query.isLoading ? (
        <Spin />
      ) : query.isError ? (
        // A load failure is an error, never rendered as an empty list or a false
        // "Not provisioned" (JAB-377 — the fan-out swallowed exactly this).
        <Alert
          type="error"
          showIcon
          message="Failed to load DNS zones"
          description="The zone inventory could not be loaded. This is usually temporary — retry shortly."
        />
      ) : query.items.length === 0 ? (
        <EmptyWithCTA description={t("dnszonesoverviewpage.no_dns_zones_yet_create_a_domain_to_manage_i")} ctaLabel="Create domain" onCta={() => navigate("/jabali-admin/domains/create")} />
      ) : (
        <SearchableTableStringQ<DnsZoneRow>
          onChange={handleTableChange}
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by domain name"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
            onChange: (page, pageSize) => query.setParams({ page, pageSize }),
          }}
        >
          <Table.Column<DnsZoneRow>
            dataIndex="name"
            title={t("dnszonesoverviewpage.domain_name")}
            key="name"
            sorter
            defaultSortOrder="ascend"
            {...columnSearchProps<DnsZoneRow>({
              placeholder: "Search by domain name",
              currentQ: query.params.q,
              onSearch: (v) => query.setParams({ q: v, page: 1 }),
            })}
          />
          <Table.Column<DnsZoneRow>
            dataIndex="username"
            title={t("dnszonesoverviewpage.owner")}
            key="username"
            sorter
            render={(username: string | null | undefined, record: DnsZoneRow) =>
              username ?? record.user_id.substring(0, 8)
            }
          />
          <Table.Column<DnsZoneRow>
            title={t("dnszonesoverviewpage.zone_status")}
            render={(_, record) =>
              record.provisioned ? (
                <Tag color="green">Provisioned</Tag>
              ) : (
                <Tag>Not provisioned</Tag>
              )
            }
          />
          <Table.Column<DnsZoneRow>
            title={t("dnszonesoverviewpage.records")}
            render={(_, record) => record.record_count ?? 0}
          />
          <Table.Column<DnsZoneRow>
            title={t("dnszonesoverviewpage.ttl")}
            render={(_, record) =>
              record.effective_ttl != null ? `${record.effective_ttl}s` : "—"
            }
          />
          <Table.Column<DnsZoneRow>
            title={t("dnszonesoverviewpage.dnssec")}
            dataIndex="dnssec_enabled"
            render={(enabled: boolean | undefined) =>
              enabled ? <Tag color="green">Signed</Tag> : <Tag>Unsigned</Tag>
            }
          />
          <Table.Column<DnsZoneRow>
            title={t("dnszonesoverviewpage.expiration")}
            dataIndex="registrar_expires_at"
            render={(d: string | null | undefined) =>
              d ? (
                <Tooltip title={t("dnszonesoverviewpage.domain_registration_expiry_from_whois")}>
                  {new Date(d).toLocaleDateString()}
                </Tooltip>
              ) : (
                <Typography.Text type="secondary">—</Typography.Text>
              )
            }
          />
          <Table.Column<DnsZoneRow>
            title={t("dnszonesoverviewpage.actions")}
            render={(_, record) => (
              <Button
                type="primary"
                onClick={() =>
                  navigate(`/jabali-admin/domains/${record.id}/dns`)
                }
              >
                Manage Records
              </Button>
            )}
          />
        </SearchableTableStringQ>
      )}
    </>
  );
};

const DNSSECTab = () => (
  <>
    <Alert
      type="info"
      showIcon
      style={{ marginBottom: 16 }}
      message="Sign zones with DNSSEC. Enable per-domain, then publish the DS record at the registrar."
      description="Signing is best-effort NSEC3 with ECDSAP256SHA256 (RFC 8624). Keys are managed by PowerDNS via pdnsutil."
    />
    <DNSSECTable showOwner />
  </>
);

export const DNSZonesOverviewPage = () => {
  const [activeTab, setActiveTab] = useTabParam<"zones" | "dnssec">("zones");
  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <ServerOutlined /> DNS Zones
      </Typography.Title>
      <Card
        tabList={[
          { key: "zones", tab: "Zones" },
          { key: "dnssec", tab: "DNSSEC" },
        ]}
        activeTabKey={activeTab}
        onTabChange={(k) => setActiveTab(k as "zones" | "dnssec")}
      >
        {activeTab === "zones" ? <ZonesTab /> : <DNSSECTab />}
      </Card>
    </div>
  );
};

// DNSZoneInventory — the shared DNS Zone Inventory Module (JAB-299).
//
// The admin and tenant DNS landing screens were near-identical copies of the
// same Card.tabList shell ("Zones" + "DNSSEC"), the same batched /dns/zones
// query, and the same columns. This module owns all of that; the two route
// shells (DNSZonesOverviewPage, UserDNSZonesOverviewPage) are thin Adapters
// that supply an audience policy.
//
// The audience policy controls only what genuinely differs between the two:
// owner-column visibility, the "Manage Records" route prefix, the empty-state
// action, the DNSSEC owner-visibility + copy, and the page header. Everything
// else — query state (URL-backed search/sort/page), the whole-list error
// branch, and the common columns — is shared so the two screens cannot drift.
import { useTranslation } from "react-i18next";
import { Alert, Button, Card, Spin, Table, Tag, Tooltip, Typography } from "antd";
import { useNavigate } from "react-router";

import { useTabParam } from "../../hooks/useTabParam";
import { columnSearchProps } from "../columnSearch";
import { DNSSECTable } from "../dnssec/DNSSECTable";
import { SearchableTableStringQ } from "../SearchableTable";
import { useTableURL } from "../../hooks/useTableURL";
import { sorterToParams } from "../../utils/tableSorter";

// DnsZoneRow is one row of the batched GET /dns/zones inventory (JAB-377):
// the domain plus its provisioning state, record count, and effective TTL,
// resolved server-side in one request instead of a per-row zone fan-out.
// `username`/`user_id` are the admin-only owner fields; the tenant inventory
// never renders them (audience.showOwner === false).
export interface DnsZoneRow {
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

// DnsZoneInventoryAudience is the per-screen policy. Admin passes the
// owner-visible, admin-routed variant; the tenant passes the owner-free,
// tenant-routed one. Nothing about the query or the common columns lives here.
export interface DnsZoneInventoryAudience {
  // showOwner adds the Owner column to the Zones table and drives the DNSSEC
  // tab's owner column (AC4 / AC5).
  showOwner: boolean;
  // manageRoute builds the "Manage Records" target for a zone row — the only
  // place the /jabali-admin vs /jabali-panel prefix differs.
  manageRoute: (zoneId: string) => string;
  // renderEmpty owns the empty-state: admin offers a create-domain CTA, the
  // tenant shows a plain Empty. Supplied by the Adapter so it can close over
  // its own navigate/copy.
  renderEmpty: () => React.ReactNode;
  // dnssec copy + owner policy for the DNSSEC tab.
  dnssec: {
    showOwner: boolean;
    message: string;
    description: string;
  };
  // header is the page title strip (icon + text). `extra` is an optional action
  // rendered opposite the title — the tenant supplies an "Add DNS Zone" button
  // (GH #1541); the admin adapter omits it (zones there are created via domains).
  header: {
    icon: React.ReactNode;
    title: string;
    extra?: React.ReactNode;
  };
}

const ZonesTab = ({ audience }: { audience: DnsZoneInventoryAudience }) => {
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
        audience.renderEmpty()
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
          {audience.showOwner && (
            <Table.Column<DnsZoneRow>
              dataIndex="username"
              title={t("dnszonesoverviewpage.owner")}
              key="username"
              sorter
              render={(username: string | null | undefined, record: DnsZoneRow) =>
                username ?? record.user_id.substring(0, 8)
              }
            />
          )}
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
              <Button type="primary" onClick={() => navigate(audience.manageRoute(record.id))}>
                Manage Records
              </Button>
            )}
          />
        </SearchableTableStringQ>
      )}
    </>
  );
};

const DnssecTab = ({ audience }: { audience: DnsZoneInventoryAudience }) => (
  <>
    <Alert
      type="info"
      showIcon
      style={{ marginBottom: 16 }}
      message={audience.dnssec.message}
      description={audience.dnssec.description}
    />
    <DNSSECTable showOwner={audience.dnssec.showOwner} />
  </>
);

// DnsZoneInventory is the whole DNS landing screen. Each route shell renders
// exactly this with its audience policy.
export const DnsZoneInventory = ({ audience }: { audience: DnsZoneInventoryAudience }) => {
  const [activeTab, setActiveTab] = useTabParam<"zones" | "dnssec">("zones");
  return (
    <div>
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          marginBottom: 16,
          flexWrap: "wrap",
          rowGap: 8,
        }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          {audience.header.icon} {audience.header.title}
        </Typography.Title>
        {audience.header.extra}
      </div>
      <Card
        tabList={[
          { key: "zones", tab: "Zones" },
          { key: "dnssec", tab: "DNSSEC" },
        ]}
        activeTabKey={activeTab}
        onTabChange={(k) => setActiveTab(k as "zones" | "dnssec")}
      >
        {activeTab === "zones" ? <ZonesTab audience={audience} /> : <DnssecTab audience={audience} />}
      </Card>
    </div>
  );
};

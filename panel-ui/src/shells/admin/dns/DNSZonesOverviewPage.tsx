// DNSZonesOverviewPage — admin landing for DNS. Card.tabList splits
// into "Zones" (per-domain provisioning) and "DNSSEC" (signing state
// + DS export). Mirrors the UserList tab style — a count Tag in each
// label, controlled activeTabKey, panel-attached strip. Both tabs
// view the same `domains` list so the badge total matches on both.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useTabParam } from "../../../hooks/useTabParam";
import { Alert, Button, Card, Spin, Table, Tag, Tooltip, Typography } from "antd";
import { ServerOutlined } from "@icons";
import { useNavigate } from "react-router";

import { apiClient } from "../../../apiClient";
import { columnSearchProps } from "../../../components/columnSearch";
import { DNSSECTable } from "../../../components/dnssec/DNSSECTable";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useTableURL } from "../../../hooks/useTableURL";

interface Domain {
  id: string;
  user_id: string;
  username?: string | null;
  name: string;
  created_at: string;
  updated_at: string;
  dnssec_enabled?: boolean;
  registrar_expires_at?: string | null;
}

interface ZoneStatus {
  provisioned: boolean;
  recordCount?: number;
  ttl?: number | null;
}

const ZonesTab = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [zoneStatuses, setZoneStatuses] = useState<Map<string, ZoneStatus>>(
    new Map(),
  );

  const query = useTableURL<Domain>({
    resource: "domains",
    defaultSort: "name",
    defaultOrder: "asc",
  });

  useEffect(() => {
    const domains = query.items;
    if (domains.length === 0) return;

    Promise.all(
      domains.map(async (domain) => {
        try {
          const res = await apiClient.get(`/domains/${domain.id}/dns/zone`);
          return {
            domainId: domain.id,
            provisioned: !!res.data?.zone?.id,
            recordCount: res.data?.record_count as number | undefined,
            ttl: (res.data?.zone?.minimum_ttl ?? null) as number | null,
          };
        } catch {
          return { domainId: domain.id, provisioned: false, recordCount: undefined, ttl: null };
        }
      }),
    ).then((results) => {
      const statusMap = new Map<string, ZoneStatus>();
      results.forEach(({ domainId, provisioned, recordCount, ttl }) => {
        statusMap.set(domainId, { provisioned, recordCount, ttl });
      });
      setZoneStatuses(statusMap);
    });
  }, [query.items]);

  const getZoneStatusTag = (domainId: string) => {
    const status = zoneStatuses.get(domainId);
    if (status === undefined) {
      return <Spin />;
    }
    return status.provisioned ? (
      <Tag color="green">Provisioned</Tag>
    ) : (
      <Tag>Not provisioned</Tag>
    );
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
      ) : query.items.length === 0 ? (
        <EmptyWithCTA description={t("dnszonesoverviewpage.no_dns_zones_yet_create_a_domain_to_manage_i")} ctaLabel="Create domain" onCta={() => navigate("/jabali-admin/domains/create")} />
      ) : (
        <SearchableTableStringQ<Domain>
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
          <Table.Column<Domain>
            dataIndex="name"
            title={t("dnszonesoverviewpage.domain_name")}
            key="name"
            sorter={{ multiple: 1 }}
            defaultSortOrder="ascend"
            {...columnSearchProps<Domain>({
              placeholder: "Search by domain name",
              currentQ: query.params.q,
              onSearch: (v) => query.setParams({ q: v, page: 1 }),
            })}
          />
          <Table.Column<Domain>
            dataIndex="username"
            title={t("dnszonesoverviewpage.owner")}
            key="username"
            sorter={{ multiple: 1 }}
            render={(username: string | null | undefined, record: Domain) =>
              username ?? record.user_id.substring(0, 8)
            }
          />
          <Table.Column<Domain>
            title={t("dnszonesoverviewpage.zone_status")}
            render={(_, record) => getZoneStatusTag(record.id)}
          />
          <Table.Column<Domain>
            title={t("dnszonesoverviewpage.records")}
            render={(_, record) => {
              const s = zoneStatuses.get(record.id);
              if (s === undefined) return <Spin size="small" />;
              return s.recordCount ?? 0;
            }}
          />
          <Table.Column<Domain>
            title={t("dnszonesoverviewpage.ttl")}
            render={(_, record) => {
              const s = zoneStatuses.get(record.id);
              if (s === undefined) return <Spin size="small" />;
              return s.ttl != null ? `${s.ttl}s` : "—";
            }}
          />
          <Table.Column<Domain>
            title={t("dnszonesoverviewpage.dnssec")}
            dataIndex="dnssec_enabled"
            render={(enabled: boolean | undefined) =>
              enabled ? <Tag color="green">Signed</Tag> : <Tag>Unsigned</Tag>
            }
          />
          <Table.Column<Domain>
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
          <Table.Column<Domain>
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

// UserDNSZonesOverviewPage — tenant landing for DNS. Card.tabList
// pattern matches admin DNS + UserList — controlled activeTabKey,
// count Tag in each tab label, panel-attached strip. Both tabs view
// the same `domains` list.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useTabParam } from "../../../hooks/useTabParam";
import { Alert, Button, Card, Empty, Spin, Table, Tag, Tooltip, Typography } from "antd";
import { CloudServerOutlined } from "@icons";
import { useNavigate } from "react-router";

import { apiClient } from "../../../apiClient";
import { columnSearchProps } from "../../../components/columnSearch";
import { DNSSECTable } from "../../../components/dnssec/DNSSECTable";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { useTableURL } from "../../../hooks/useTableURL";

interface Domain {
  id: string;
  user_id: string;
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
            // GH #527: show the effective default record TTL (what records
            // use), not the SOA minimum_ttl (negative-cache timer, fixed 3600).
            ttl: (res.data?.effective_ttl ?? null) as number | null,
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
        title={t("userdnszonesoverviewpage.dns_zones_are_provisioned_automatically_when")}
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
      />
      {query.isLoading ? (
        <Spin />
      ) : query.items.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("userdnszonesoverviewpage.no_domains_found")} />
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
            title={t("userdnszonesoverviewpage.domain_name")}
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
            title={t("userdnszonesoverviewpage.zone_status")}
            render={(_, record) => getZoneStatusTag(record.id)}
          />
          <Table.Column<Domain>
            title={t("userdnszonesoverviewpage.records")}
            render={(_, record) => {
              const s = zoneStatuses.get(record.id);
              if (s === undefined) return <Spin size="small" />;
              return s.recordCount ?? 0;
            }}
          />
          <Table.Column<Domain>
            title={t("userdnszonesoverviewpage.ttl")}
            render={(_, record) => {
              const s = zoneStatuses.get(record.id);
              if (s === undefined) return <Spin size="small" />;
              return s.ttl != null ? `${s.ttl}s` : "—";
            }}
          />
          <Table.Column<Domain>
            title={t("userdnszonesoverviewpage.dnssec")}
            dataIndex="dnssec_enabled"
            render={(enabled: boolean | undefined) =>
              enabled ? <Tag color="green">Signed</Tag> : <Tag>Unsigned</Tag>
            }
          />
          <Table.Column<Domain>
            title={t("userdnszonesoverviewpage.expiration")}
            dataIndex="registrar_expires_at"
            render={(d: string | null | undefined) =>
              d ? (
                <Tooltip title={t("userdnszonesoverviewpage.domain_registration_expiry_from_whois")}>
                  {new Date(d).toLocaleDateString()}
                </Tooltip>
              ) : (
                <Typography.Text type="secondary">—</Typography.Text>
              )
            }
          />
          <Table.Column<Domain>
            title={t("userdnszonesoverviewpage.actions")}
            render={(_, record) => (
              <Button
                type="primary"
                onClick={() =>
                  navigate(`/jabali-panel/domains/${record.id}/dns`)
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
      message="Protect your domain with DNSSEC."
      description="Enable signing here, then copy the DS record to your registrar to complete the chain of trust."
    />
    <DNSSECTable showOwner={false} />
  </>
);

export const UserDNSZonesOverviewPage = () => {
  const [activeTab, setActiveTab] = useTabParam<"zones" | "dnssec">("zones");
  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <CloudServerOutlined /> DNS
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

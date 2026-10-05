// DNSZoneInventory — the shared DNS Zone Inventory Module (JAB-299).
//
// The admin and tenant DNS landing screens were near-identical copies of the
// same shell, the same batched /dns/zones query, and the same columns. This
// module owns all of that; the two route shells (DNSZonesOverviewPage,
// UserDNSZonesOverviewPage) are thin Adapters that supply an audience policy.
//
// GH #1918 (johnnyq): the separate DNSSEC tab is gone. The domain name opens
// the zone's records (no "Manage Records" button), and the row's ⋯ menu holds
// Enable / Disable DNSSEC, View DS & keys, and the zone (or domain) delete —
// the same name-link + ⋯ shape as the Mail Domains list (GH #1387).
//
// The audience policy controls only what genuinely differs between the two:
// owner-column visibility, the records route prefix, the empty-state action,
// the DNSSEC keys note, and the page header. Everything else — query state
// (URL-backed search/sort/page), the whole-list error branch, and the common
// columns — is shared so the two screens cannot drift.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Alert, Button, Card, Dropdown, Spin, Table, Tag, Tooltip, Typography } from "antd";
import type { MenuProps } from "antd";
import { Link } from "react-router";
import { DeleteOutlined, KeyOutlined, MoreOutlined, SafetyOutlined } from "@icons";

import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { useSetDNSSEC } from "../../hooks/useDNSSEC";
import { columnSearchProps } from "../columnSearch";
import { DNSSECKeysModal } from "../dnssec/DNSSECKeysModal";
import { DNSZoneDeleteAction } from "./DNSZoneDeleteAction";
import { DNSDomainDeleteAction } from "./DNSDomainDeleteAction";
import { DNSZoneEnableButton } from "./DNSZoneEnableButton";
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
  // GH #1611: facet state. dns_disabled distinguishes a deliberately-dropped
  // zone ("host DNS elsewhere" → offer Enable DNS) from one the reconciler
  // simply hasn't provisioned yet. web_disabled + email_enabled identify a
  // DNS-only domain (both off) whose zone can't be dropped alone (last facet) —
  // that row offers a whole-domain delete instead.
  dns_disabled?: boolean;
  web_disabled?: boolean;
  email_enabled?: boolean;
}

// DnsZoneInventoryAudience is the per-screen policy. Admin passes the
// owner-visible, admin-routed variant; the tenant passes the owner-free,
// tenant-routed one. Nothing about the query or the common columns lives here.
export interface DnsZoneInventoryAudience {
  // showOwner adds the Owner column to the zone table (AC4).
  showOwner: boolean;
  // manageRoute builds the records page a zone's name links to — the only
  // place the /jabali-admin vs /jabali-panel prefix differs.
  manageRoute: (zoneId: string) => string;
  // renderEmpty owns the empty-state: admin offers a create-domain CTA, the
  // tenant shows a plain Empty. Supplied by the Adapter so it can close over
  // its own navigate/copy.
  renderEmpty: () => React.ReactNode;
  // dnssecNote is optional copy under the keys in the "View DS & keys" modal —
  // the admin adapter says how signing is done; the tenant omits it.
  dnssecNote?: React.ReactNode;
  // header is the page title strip (icon + text). `extra` is an optional action
  // rendered opposite the title — the tenant supplies an "Add DNS Zone" button
  // (GH #1541); the admin adapter omits it (zones there are created via domains).
  header: {
    icon: React.ReactNode;
    title: string;
    extra?: React.ReactNode;
  };
}

// apiErrorText pulls the API's detail/error out of a failed request.
const apiErrorText = (err: unknown, fallback: string) =>
  (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data?.detail ??
  (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
  (err instanceof Error ? err.message : fallback);

const ZoneTable = ({ audience }: { audience: DnsZoneInventoryAudience }) => {
  const { t } = useTranslation();
  // GH #1611: the row whose DNS zone is being deleted (null = closed). Admin +
  // tenant both get the action; the backend enforces admin-or-owner.
  const [deleteTarget, setDeleteTarget] = useState<DnsZoneRow | null>(null);
  // GH #1611: the DNS-only row whose WHOLE domain is being deleted (its zone
  // can't be dropped alone — DNS is the last facet).
  const [deleteDomainTarget, setDeleteDomainTarget] = useState<DnsZoneRow | null>(null);
  // GH #1918: the signed zone whose DS records + keys are open (null = closed).
  const [keysTarget, setKeysTarget] = useState<DnsZoneRow | null>(null);
  // The row whose DNSSEC flip is in flight — spins that row's ⋯ trigger.
  const [dnssecBusyId, setDnssecBusyId] = useState<string | null>(null);
  const setDNSSEC = useSetDNSSEC();

  const flipDNSSEC = async (zone: DnsZoneRow, enabled: boolean) => {
    setDnssecBusyId(zone.id);
    try {
      await setDNSSEC.mutateAsync({ domainID: zone.id, enabled });
      feedback.message.success(
        enabled
          ? `DNSSEC enabled for ${zone.name}. Publish its DS record at your registrar (⋯ → View DS & keys).`
          : `DNSSEC disabled for ${zone.name}.`,
      );
    } catch (err) {
      feedback.message.error(apiErrorText(err, "Could not change DNSSEC"));
    } finally {
      setDnssecBusyId(null);
    }
  };

  // Disabling signing while the registrar still publishes the DS makes
  // validating resolvers reject the zone, so it confirms first. Enabling is
  // harmless until a DS is published, so it does not.
  const confirmDisableDNSSEC = (zone: DnsZoneRow) => {
    feedback.modal.confirm({
      title: `Disable DNSSEC for ${zone.name}?`,
      content:
        "Remove the DS record at your registrar first. While the registrar still " +
        "publishes it, resolvers that check DNSSEC reject the unsigned zone, and " +
        "the domain stops resolving for their users.",
      okText: "Disable DNSSEC",
      okButtonProps: { danger: true },
      onOk: () => flipDNSSEC(zone, false),
    });
  };

  const rowMenu = (record: DnsZoneRow): MenuProps["items"] => {
    // GH #1611: a DNS-only domain (web off + mail off) can't drop its zone
    // alone — DNS is the last facet — so it offers a whole-domain delete
    // instead of the zone delete the backend would refuse.
    const dnsOnly = record.web_disabled === true && record.email_enabled !== true;
    const items: NonNullable<MenuProps["items"]> = [];
    // Signing needs a zone in PowerDNS (the agent runs pdnsutil on it). A
    // signed row always offers Disable, so a stuck state can be cleared.
    if (record.dnssec_enabled) {
      items.push(
        {
          key: "dnssec-keys",
          icon: <KeyOutlined />,
          label: "View DS & keys",
          onClick: () => setKeysTarget(record),
        },
        {
          key: "dnssec-off",
          icon: <SafetyOutlined />,
          danger: true,
          label: "Disable DNSSEC",
          onClick: () => confirmDisableDNSSEC(record),
        },
      );
    } else if (record.provisioned) {
      items.push({
        key: "dnssec-on",
        icon: <SafetyOutlined />,
        label: "Enable DNSSEC",
        onClick: () => void flipDNSSEC(record, true),
      });
    }
    if (dnsOnly) {
      items.push({
        key: "delete-domain",
        icon: <DeleteOutlined />,
        danger: true,
        label: t("dnszonesoverviewpage.delete_domain"),
        onClick: () => setDeleteDomainTarget(record),
      });
    } else if (record.provisioned) {
      // Delete the DNS zone (keep web + mail). Only for a panel-hosted zone; a
      // DNSSEC-signed zone must be unsigned first (the backend refuses it too).
      items.push({
        key: "delete-zone",
        icon: <DeleteOutlined />,
        danger: true,
        disabled: record.dnssec_enabled === true,
        label: record.dnssec_enabled ? (
          <span>
            {t("dnszonesoverviewpage.delete_zone")}
            <br />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t("dnszonesoverviewpage.delete_disabled_dnssec")}
            </Typography.Text>
          </span>
        ) : (
          t("dnszonesoverviewpage.delete_zone")
        ),
        onClick: () => setDeleteTarget(record),
      });
    }
    return items;
  };

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
            // GH #1918: the name opens the zone's records. While DNS is
            // dropped there are no records to manage, so it stays plain text.
            render={(name: string, record: DnsZoneRow) =>
              record.dns_disabled ? name : <Link to={audience.manageRoute(record.id)}>{name}</Link>
            }
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
              record.dns_disabled ? (
                // GH #1611: DNS was deliberately dropped — distinct from a zone
                // the reconciler simply hasn't provisioned yet, which would
                // otherwise read as the same "Not provisioned".
                <Tag color="orange">{t("dnszonesoverviewpage.dns_hosted_elsewhere")}</Tag>
              ) : record.provisioned ? (
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
            render={(_, record) => {
              // GH #1611: DNS was dropped ("host DNS elsewhere"). Enable DNS is
              // that row's only action, so it stays a visible button rather
              // than a one-item ⋯ menu.
              if (record.dns_disabled) {
                return <DNSZoneEnableButton zone={record} />;
              }
              const items = rowMenu(record);
              if (!items || items.length === 0) {
                return <Typography.Text type="secondary">—</Typography.Text>;
              }
              return (
                <Dropdown trigger={["click"]} menu={{ items }}>
                  <Button
                    size="small"
                    icon={<MoreOutlined />}
                    loading={dnssecBusyId === record.id}
                    aria-label={`Actions for ${record.name}`}
                  />
                </Dropdown>
              );
            }}
          />
        </SearchableTableStringQ>
      )}
      {deleteTarget && (
        <DNSZoneDeleteAction
          zone={deleteTarget}
          open={deleteTarget != null}
          onClose={() => setDeleteTarget(null)}
        />
      )}
      {deleteDomainTarget && (
        <DNSDomainDeleteAction
          zone={deleteDomainTarget}
          open={deleteDomainTarget != null}
          onClose={() => setDeleteDomainTarget(null)}
        />
      )}
      <DNSSECKeysModal
        domainID={keysTarget?.id ?? null}
        domainName={keysTarget?.name ?? ""}
        open={keysTarget != null}
        onClose={() => setKeysTarget(null)}
        note={audience.dnssecNote}
      />
    </>
  );
};

// DnsZoneInventory is the whole DNS landing screen. Each route shell renders
// exactly this with its audience policy.
export const DnsZoneInventory = ({ audience }: { audience: DnsZoneInventoryAudience }) => {
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
      <Card>
        <ZoneTable audience={audience} />
      </Card>
    </div>
  );
};

// DNSZonesOverviewPage — admin Adapter for the shared DNS Zone Inventory
// Module (JAB-299). The page owns only the admin audience policy: owner column
// visible, admin domain routes, a create-domain empty-state CTA, and the
// owner-visible DNSSEC tab. All list/query/column behavior lives in
// components/dns/DNSZoneInventory.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Button } from "antd";
import { ServerOutlined, PlusOutlined } from "@icons";

import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import {
  DnsZoneInventory,
  type DnsZoneInventoryAudience,
} from "../../../components/dns/DNSZoneInventory";
import { AdminDNSZoneDrawer } from "./AdminDNSZoneDrawer";

export const DNSZonesOverviewPage = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);

  const audience: DnsZoneInventoryAudience = {
    showOwner: true,
    manageRoute: (id) => `/jabali-admin/domains/${id}/dns`,
    renderEmpty: () => (
      <EmptyWithCTA
        description={t("dnszonesoverviewpage.no_dns_zones_yet_create_a_domain_to_manage_i")}
        ctaLabel="Create domain"
        onCta={() => navigate("/jabali-admin/domains/create")}
      />
    ),
    dnssec: {
      showOwner: true,
      message: "Sign zones with DNSSEC. Enable per-domain, then publish the DS record at the registrar.",
      description:
        "Signing is best-effort NSEC3 with ECDSAP256SHA256 (RFC 8624). Keys are managed by PowerDNS via pdnsutil.",
    },
    header: {
      icon: <ServerOutlined />,
      title: "DNS Zones",
      // GH #1540: Add DNS Zone on the admin list too — opens the admin drawer
      // (Owner picker + Domain Name + IP + Template).
      extra: (
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
          Add DNS Zone
        </Button>
      ),
    },
  };

  return (
    <>
      <DnsZoneInventory audience={audience} />
      <AdminDNSZoneDrawer
        open={createOpen}
        onClose={() => {
          setCreateOpen(false);
          // The drawer creates via the "domains" resource (invalidates
          // ["list","domains"]); this page lists ["list","dns/zones"], so refresh
          // that list explicitly to show the newly added zone.
          void qc.invalidateQueries({ queryKey: ["list", "dns/zones"] });
        }}
      />
    </>
  );
};

// DNSZonesOverviewPage — admin Adapter for the shared DNS Zone Inventory
// Module (JAB-299). The page owns only the admin audience policy: owner column
// visible, admin domain routes, a create-domain empty-state CTA, and the
// owner-visible DNSSEC tab. All list/query/column behavior lives in
// components/dns/DNSZoneInventory.
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { ServerOutlined } from "@icons";

import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import {
  DnsZoneInventory,
  type DnsZoneInventoryAudience,
} from "../../../components/dns/DNSZoneInventory";

export const DNSZonesOverviewPage = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();

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
    header: { icon: <ServerOutlined />, title: "DNS Zones" },
  };

  return <DnsZoneInventory audience={audience} />;
};

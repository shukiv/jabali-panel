// UserDNSZonesOverviewPage — tenant Adapter for the shared DNS Zone Inventory
// Module (JAB-299). The page owns only the tenant audience policy: no owner
// column, tenant domain routes, a plain empty state, and the owner-free DNSSEC
// tab. All list/query/column behavior lives in components/dns/DNSZoneInventory.
import { useTranslation } from "react-i18next";
import { Empty } from "antd";
import { CloudServerOutlined } from "@icons";

import {
  DnsZoneInventory,
  type DnsZoneInventoryAudience,
} from "../../../components/dns/DNSZoneInventory";

export const UserDNSZonesOverviewPage = () => {
  const { t } = useTranslation();

  const audience: DnsZoneInventoryAudience = {
    showOwner: false,
    manageRoute: (id) => `/jabali-panel/domains/${id}/dns`,
    renderEmpty: () => (
      <Empty
        image={Empty.PRESENTED_IMAGE_SIMPLE}
        description={t("userdnszonesoverviewpage.no_domains_found")}
      />
    ),
    dnssec: {
      showOwner: false,
      message: "Protect your domain with DNSSEC.",
      description:
        "Enable signing here, then copy the DS record to your registrar to complete the chain of trust.",
    },
    header: { icon: <CloudServerOutlined />, title: "DNS" },
  };

  return <DnsZoneInventory audience={audience} />;
};

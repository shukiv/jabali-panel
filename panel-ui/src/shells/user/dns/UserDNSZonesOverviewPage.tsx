// UserDNSZonesOverviewPage — tenant Adapter for the shared DNS Zone Inventory
// Module (JAB-299). The page owns only the tenant audience policy: no owner
// column, tenant domain routes, a plain empty state, and the owner-free DNSSEC
// tab. All list/query/column behavior lives in components/dns/DNSZoneInventory.
//
// GH #1541 (johnnyq): DNS-only zones used to be added from the Domains page
// "Add" split, which is gone. The "add these later" path now lives here — an
// "Add DNS Zone" button that opens the shared Add-domain drawer in dns mode.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Button, Empty } from "antd";
import { CloudServerOutlined, PlusOutlined } from "@icons";

import {
  DnsZoneInventory,
  type DnsZoneInventoryAudience,
} from "../../../components/dns/DNSZoneInventory";
import { UserDomainDrawer } from "../domains/UserDomainDrawer";

export const UserDNSZonesOverviewPage = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);

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
    header: {
      icon: <CloudServerOutlined />,
      title: "DNS",
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
      <UserDomainDrawer
        open={createOpen}
        mode="dns"
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

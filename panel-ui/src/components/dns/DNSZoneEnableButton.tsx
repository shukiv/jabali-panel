// DNSZoneEnableButton — re-enable panel-hosted DNS for a domain whose zone was
// previously dropped (GH #1611, "host DNS here again"). POST /domains/:id/dns/zone
// is admin-or-owner; it flips dns_disabled=false and the reconciler re-creates
// the zone and its records on its next tick.
//
// This is the inverse of DNSZoneDeleteAction. It is non-destructive, so it uses a
// short confirm rather than a full warning modal — the only caveat worth naming
// is that the panel will start answering DNS for the domain again, which can
// conflict with records the tenant published wherever they moved DNS.
import { useState } from "react";
import { Button } from "antd";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import type { DnsZoneRow } from "./DNSZoneInventory";

interface DNSZoneEnableButtonProps {
  zone: DnsZoneRow;
}

export const DNSZoneEnableButton = ({ zone }: DNSZoneEnableButtonProps) => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [isLoading, setIsLoading] = useState(false);

  const handleEnable = () => {
    feedback.modal.confirm({
      title: `Enable DNS for "${zone.name}"?`,
      content:
        "The panel will host DNS for this domain again and re-create its zone " +
        "with default records on the next sync. If you moved DNS elsewhere, the " +
        "panel's records will now compete with it — remove the domain from your " +
        "other DNS host first.",
      okText: t("dnszonesoverviewpage.enable_dns"),
      onOk: async () => {
        setIsLoading(true);
        try {
          await apiClient.post(`/domains/${encodeURIComponent(zone.id)}/dns/zone`);
          feedback.message.success(
            `Re-enabled DNS for "${zone.name}". The zone is re-created within a minute.`,
          );
          qc.invalidateQueries({ queryKey: ["list", "dns/zones"] });
        } catch (err: unknown) {
          const errMsg =
            (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data
              ?.detail ??
            (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
            (err instanceof Error ? err.message : "Enable DNS failed");
          feedback.message.error(errMsg);
        } finally {
          setIsLoading(false);
        }
      },
    });
  };

  return (
    <Button loading={isLoading} onClick={handleEnable}>
      {t("dnszonesoverviewpage.enable_dns")}
    </Button>
  );
};

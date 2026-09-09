// DNSZoneDeleteAction — controlled confirm modal that deletes a domain's DNS
// zone while keeping the Web Domain and Mail Domain (GH #1611, johnnyq). The
// panel stops hosting DNS for the domain ("host DNS elsewhere"); web + mail are
// untouched.
//
// DELETE /domains/:id/dns/zone is admin-or-owner. The backend refuses (4xx with
// a `detail`) a DNSSEC-signed zone, the panel's own primary domain, and a domain
// whose only remaining facet is DNS (delete the whole domain instead); those
// messages are surfaced verbatim.
import { useState } from "react";
import { Alert, Modal, Typography } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import type { DnsZoneRow } from "./DNSZoneInventory";

interface DNSZoneDeleteActionProps {
  zone: DnsZoneRow;
  open: boolean;
  onClose: () => void;
}

export const DNSZoneDeleteAction = ({ zone, open, onClose }: DNSZoneDeleteActionProps) => {
  const qc = useQueryClient();
  const [isLoading, setIsLoading] = useState(false);

  const handleDelete = async () => {
    setIsLoading(true);
    try {
      await apiClient.delete(`/domains/${encodeURIComponent(zone.id)}/dns/zone`);
      feedback.message.success(
        `Deleted the DNS zone for "${zone.name}". Web and mail are unchanged.`,
      );
      qc.invalidateQueries({ queryKey: ["list", "dns/zones"] });
      onClose();
    } catch (err: unknown) {
      const errMsg =
        (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data
          ?.detail ??
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        (err instanceof Error ? err.message : "Delete DNS zone failed");
      feedback.message.error(errMsg);
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <Modal
      title="Delete DNS zone"
      open={open}
      onCancel={onClose}
      onOk={handleDelete}
      okText="Delete DNS zone"
      okButtonProps={{ danger: true }}
      confirmLoading={isLoading}
      destroyOnHidden
    >
      <Typography.Paragraph>
        This removes the panel-hosted DNS zone for <b>{zone.name}</b>. The Web Domain and
        Mail Domain keep working — only DNS hosting moves off the panel.
      </Typography.Paragraph>
      <Alert
        type="warning"
        showIcon
        message="Copy your records first — this can't be undone"
        description={
          <>
            The zone and all {zone.record_count} of its records are deleted. If this domain
            sends or receives mail, copy its <b>MX, SPF, DKIM and DMARC</b> records (Manage
            Records) and publish them at your new DNS host first, or mail will break.
          </>
        }
      />
    </Modal>
  );
};

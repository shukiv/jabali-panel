// DNSDomainDeleteAction — controlled confirm modal that deletes an ENTIRE domain
// from the DNS Zone inventory (GH #1611, johnnyq). This is deliberately separate
// from DNSZoneDeleteAction: it is offered only for a DNS-only domain (web off,
// mail off), whose DNS facet cannot be dropped on its own — DNS is the last
// facet, so the backend refuses the zone delete and points here. Deleting it
// removes the whole domain row via DELETE /domains/:id (userops.DeleteDomain),
// not just the zone, so the copy and title say so plainly rather than reusing the
// "Delete DNS zone" modal.
import { useState } from "react";
import { Alert, Modal, Typography } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import type { DnsZoneRow } from "./DNSZoneInventory";

interface DNSDomainDeleteActionProps {
  zone: DnsZoneRow;
  open: boolean;
  onClose: () => void;
}

export const DNSDomainDeleteAction = ({ zone, open, onClose }: DNSDomainDeleteActionProps) => {
  const qc = useQueryClient();
  const [isLoading, setIsLoading] = useState(false);

  const handleDelete = async () => {
    setIsLoading(true);
    try {
      // Full domain delete — no delete_files/delete_mail query params: this row is
      // web-off + mail-off, so DNS is all that remains.
      await apiClient.delete(`/domains/${encodeURIComponent(zone.id)}`);
      feedback.message.success(`Deleted the domain "${zone.name}".`);
      qc.invalidateQueries({ queryKey: ["list", "dns/zones"] });
      onClose();
    } catch (err: unknown) {
      const errMsg =
        (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data
          ?.detail ??
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        (err instanceof Error ? err.message : "Delete domain failed");
      feedback.message.error(errMsg);
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <Modal
      title="Delete domain"
      open={open}
      onCancel={onClose}
      onOk={handleDelete}
      okText="Delete domain"
      okButtonProps={{ danger: true }}
      confirmLoading={isLoading}
      destroyOnHidden
    >
      <Typography.Paragraph>
        <b>{zone.name}</b> has only DNS — no Web Domain and no Mail Domain. Deleting its zone
        alone would leave an empty domain, so this removes the <b>whole domain</b>.
      </Typography.Paragraph>
      <Alert
        type="warning"
        showIcon
        message="This deletes the domain entirely and can't be undone"
        description={
          <>
            The domain, its DNS zone, and all {zone.record_count} of its records are removed. If
            you host DNS for this domain elsewhere, this only removes it from the panel — but the
            panel entry and any panel history for it are gone.
          </>
        }
      />
    </Modal>
  );
};

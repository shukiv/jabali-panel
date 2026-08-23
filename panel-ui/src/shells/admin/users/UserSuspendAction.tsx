// UserSuspendAction — controlled modal that toggles a user's online
// state. Suspending flips users.suspended=1, pushes the Kratos identity
// to state=inactive (blocks panel + webmail + every Kratos-fronted UI
// on next request) and bulk-disables every owned domain (reconciler
// drops the nginx sites-enabled symlinks on next tick so all sites
// serve 404). Unsuspending reverses all three. Reason is operator-
// facing audit text visible on the row. Parent (UserList RowActions)
// drives `open` via its dropdown menu.
import { useState } from "react";
import { Input, Modal } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

interface UserSuspendActionProps {
  userId: string;
  // Display identifier — username-led (see userLabel, GH #1239).
  userLabel: string;
  suspended: boolean;
  open: boolean;
  onClose: () => void;
}

export const UserSuspendAction = ({
  userId,
  userLabel,
  suspended,
  open,
  onClose,
}: UserSuspendActionProps) => {
  const qc = useQueryClient();
  const [isLoading, setIsLoading] = useState(false);
  const [reason, setReason] = useState("");

  const handleClose = () => {
    setReason("");
    onClose();
  };

  const handleSubmit = async () => {
    setIsLoading(true);
    try {
      const endpoint = suspended ? "unsuspend" : "suspend";
      const body = suspended ? undefined : { reason };
      const res = await apiClient.post<{
        ok: boolean;
        domains_disabled?: number;
        domains_enabled?: number;
        kratos_warning?: string;
        domain_warning?: string;
      }>(`/admin/users/${encodeURIComponent(userId)}/${endpoint}`, body);
      const data = res.data;
      if (suspended) {
        feedback.message.success(
          `Unsuspended "${userLabel}" — ${data.domains_enabled ?? 0} domain(s) re-enabled.`,
        );
      } else {
        feedback.message.success(
          `Suspended "${userLabel}" — ${data.domains_disabled ?? 0} domain(s) disabled.`,
        );
      }
      if (data.kratos_warning) {
        feedback.message.warning(`Kratos: ${data.kratos_warning}`);
      }
      if (data.domain_warning) {
        feedback.message.warning(`Domains: ${data.domain_warning}`);
      }
      qc.invalidateQueries({ queryKey: ["list", "users"] });
      handleClose();
    } catch (err: unknown) {
      const errMsg =
        (err as { response?: { data?: { detail?: string; error?: string } } })
          ?.response?.data?.detail ??
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err instanceof Error ? err.message : "Action failed");
      feedback.message.error(errMsg);
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <Modal
      title={suspended ? "Unsuspend user?" : "Suspend user?"}
      open={open}
      onCancel={handleClose}
      onOk={handleSubmit}
      confirmLoading={isLoading}
      okText={suspended ? "Unsuspend" : "Suspend"}
      okButtonProps={{ danger: !suspended }}
    >
      {suspended ? (
        <p>
          Restores access for <strong>{userLabel}</strong>. The Kratos
          identity is reactivated and every owned domain is re-enabled.
        </p>
      ) : (
        <>
          <p>
            Takes <strong>{userLabel}</strong> offline:
          </p>
          <ul>
            <li>Kratos identity → inactive (blocks panel + webmail login)</li>
            <li>All owned domains disabled (sites serve 404)</li>
          </ul>
          <p>Optional reason (visible in the user list):</p>
          <Input.TextArea
            rows={2}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="e.g. non-payment, ToS violation"
            maxLength={255}
          />
        </>
      )}
    </Modal>
  );
};

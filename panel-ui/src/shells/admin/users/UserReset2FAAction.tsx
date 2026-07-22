// UserReset2FAAction — controlled modal that strips TOTP + recovery
// codes from a user's Kratos identity. Used when a user has lost their
// authenticator AND burned through their recovery codes. The user
// keeps their password; on next login they're at aal1 and can re-enrol
// from /profile. The parent (UserList RowActions) drives `open` via
// its dropdown menu so the modal lives alongside the row's other
// action modals and the dropdown items can use stock AntD styling.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { Modal, message } from "antd";

import { apiClient } from "../../../apiClient";

interface UserReset2FAActionProps {
  userId: string;
  userEmail: string;
  open: boolean;
  onClose: () => void;
}

export const UserReset2FAAction = ({
  userId,
  userEmail,
  open,
  onClose,
}: UserReset2FAActionProps) => {
  const { t } = useTranslation();
  const [isLoading, setIsLoading] = useState(false);

  const handleReset = async () => {
    setIsLoading(true);
    try {
      await apiClient.post(`/admin/users/${encodeURIComponent(userId)}/2fa/reset`);
      message.success(`Two-factor authentication reset for "${userEmail}"`);
      onClose();
    } catch (err: unknown) {
      const errMsg =
        err instanceof Error ? err.message : "Failed to reset two-factor authentication";
      message.error(errMsg);
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <Modal
      title={t("userreset2faaction.reset_two_factor_authentication")}
      open={open}
      onCancel={onClose}
      onOk={handleReset}
      confirmLoading={isLoading}
      okText={t("userreset2faaction.reset")}
      okButtonProps={{ danger: true }}
    >
      <p>
        Removes the TOTP authenticator and recovery codes from{" "}
        <strong>{userEmail}</strong>. The user keeps their password and can
        re-enrol from their profile page after their next sign-in.
      </p>
      <p>Use only when the user has confirmed they cannot recover access.</p>
    </Modal>
  );
};

// UserResetPasswordAction — admin sets a fresh password for a user and
// reveals it once. Under username-only login (M54) email is a plain trait,
// so Kratos has no self-service recovery; this admin-set flow is the only
// password reset. Mirrors UserReset2FAAction's controlled-modal shape; the
// parent (UserList RowActions) drives `open`.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { Alert, Modal, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../../../apiClient";

interface Props {
  userId: string;
  // Display identifier — username-led (see userLabel, GH #1239).
  userLabel: string;
  open: boolean;
  onClose: () => void;
}

export const UserResetPasswordAction = ({ userId, userLabel, open, onClose }: Props) => {
  const { t } = useTranslation();
  const [isLoading, setIsLoading] = useState(false);
  const [tempPassword, setTempPassword] = useState<string | null>(null);
  // GH #2058: false when the panel password changed but the server account's
  // (SSH, SFTP) didn't.
  const [osSynced, setOsSynced] = useState(true);

  const handleReset = async () => {
    setIsLoading(true);
    try {
      const res = await apiClient.post<{ password: string; os_synced?: boolean }>(
        `/admin/users/${encodeURIComponent(userId)}/password/reset`,
      );
      setTempPassword(res.data.password);
      setOsSynced(res.data.os_synced !== false);
      feedback.message.success(`Password reset for "${userLabel}"`);
    } catch (err: unknown) {
      feedback.message.error(err instanceof Error ? err.message : "Failed to reset password");
    } finally {
      setIsLoading(false);
    }
  };

  const close = () => {
    setTempPassword(null);
    setOsSynced(true);
    onClose();
  };

  if (tempPassword !== null) {
    return (
      <Modal
        title={t("userresetpasswordaction.new_password_shown_once")}
        open={open}
        onCancel={close}
        onOk={close}
        cancelButtonProps={{ style: { display: "none" } }}
        okText={t("userresetpasswordaction.done")}
      >
        <p>
          Hand this temporary password to <strong>{userLabel}</strong>. It is shown
          only once. They sign in with it (the panel, and SSH or SFTP) and should
          change it from their profile.
        </p>
        {!osSynced && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message="SSH and SFTP still use the old password"
            description="The panel password changed, but the server account's password couldn't be updated. Reset again to retry; the panel log has the reason."
          />
        )}
        <Typography.Paragraph copyable strong style={{ fontSize: 16 }}>
          {tempPassword}
        </Typography.Paragraph>
      </Modal>
    );
  }

  return (
    <Modal
      title={t("userresetpasswordaction.reset_password")}
      open={open}
      onCancel={close}
      onOk={handleReset}
      confirmLoading={isLoading}
      okText={t("userresetpasswordaction.reset")}
      okButtonProps={{ danger: true }}
    >
      <p>
        Sets a new temporary password for <strong>{userLabel}</strong> and shows it
        once. Their current password stops working immediately.
      </p>
      <p>There is no self-service recovery under username login — this is the reset.</p>
    </Modal>
  );
};

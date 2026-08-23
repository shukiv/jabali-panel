// UserDeleteAction — controlled destructive confirmation modal.
// There is no longer a "preserve files" mode; deleting a user always
// removes everything they own (domains, databases, mailboxes, cron
// jobs, OS account, /home, related rows). Parent (UserList RowActions)
// drives `open` via its dropdown menu.
import { useState } from "react";
import { Button, Modal } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

interface UserDeleteActionProps {
  recordItemId: string;
  // Display identifier — username-led (see userLabel, GH #1239).
  userLabel: string;
  // POSIX account name (M54). Absent only for legacy rows created
  // before usernames existed — then the email local part is the account.
  username?: string | null;
  userEmail: string;
  open: boolean;
  onClose: () => void;
}

export const UserDeleteAction = ({
  recordItemId,
  userLabel,
  username,
  userEmail,
  open,
  onClose,
}: UserDeleteActionProps) => {
  const [isLoading, setIsLoading] = useState(false);
  const qc = useQueryClient();

  const handleDelete = async () => {
    setIsLoading(true);
    try {
      await apiClient.delete(`/users/${encodeURIComponent(recordItemId)}`);

      feedback.message.success(`User "${userLabel}" and all related data deleted`);

      // Invalidate every ["list", "users", *] variant so admin tabs
      // and the parent badge counters all refetch after a delete.
      qc.invalidateQueries({ queryKey: ["list", "users"] });
      qc.invalidateQueries({ queryKey: ["one", "users", recordItemId] });

      onClose();
    } catch (err: unknown) {
      const errMsg =
        err instanceof Error ? err.message : "Failed to delete user";
      feedback.message.error(errMsg);
    } finally {
      setIsLoading(false);
    }
  };

  const osAccount = username || userEmail.split("@")[0];

  return (
    <Modal
      title={`Delete user "${userLabel}"?`}
      open={open}
      onCancel={onClose}
      footer={[
        <Button key="cancel" onClick={onClose}>
          Cancel
        </Button>,
        <Button
          key="delete"
          danger
          type="primary"
          loading={isLoading}
          onClick={handleDelete}
        >
          Delete user
        </Button>,
      ]}
    >
      <p>
        This is permanent and cannot be undone. Deleting{" "}
        <strong>{userLabel}</strong> will remove:
      </p>
      <ul>
        <li>All owned domains, DNS zones, SSL certificates, nginx sites</li>
        <li>All databases and database users (panel + MariaDB)</li>
        <li>All mailboxes, forwarders, and Stalwart mail accounts</li>
        <li>All cron jobs, applications, SSH keys</li>
        <li>
          The OS account <code>{osAccount}</code> and <code>/home/{osAccount}</code>
        </li>
        <li>The Kratos identity (login record)</li>
      </ul>
    </Modal>
  );
};

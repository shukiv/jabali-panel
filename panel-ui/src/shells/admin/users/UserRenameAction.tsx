// UserRenameAction — controlled modal that renames a tenant's Linux/login
// username in place (GH #1238, WebUI for `jabali user rename`).
//
// The uid is preserved, so files aren't re-chowned; the account name + home
// path move and the owned domains re-render (on the reconciler's next tick).
// POST /admin/users/:id/rename is admin-only AND behind the JAB-380 step-up —
// a stale session gets a 403 that apiClient turns into a re-auth + retry.
//
// v1 refuses a tenant with FTP/SFTP subaccounts or Python apps; databases + DB
// SSO roles keep the old-username prefix (keyed by id, so they keep working).
// The backend returns those refusals as a 422 with a `detail` message, which we
// surface verbatim.
import { useState } from "react";
import { Alert, Form, Input, Modal, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

// Mirror of the agent/userops POSIX-username rule.
const USERNAME_RE = /^[a-z_][a-z0-9_-]{0,31}$/;

interface UserRenameActionProps {
  userId: string;
  currentUsername?: string | null;
  userLabel: string;
  open: boolean;
  onClose: () => void;
}

export const UserRenameAction = ({
  userId,
  currentUsername,
  userLabel,
  open,
  onClose,
}: UserRenameActionProps) => {
  const qc = useQueryClient();
  const [isLoading, setIsLoading] = useState(false);
  const [newUsername, setNewUsername] = useState("");

  const trimmed = newUsername.trim();
  const invalid = !USERNAME_RE.test(trimmed);
  const unchanged = !!currentUsername && trimmed === currentUsername;
  const okDisabled = trimmed === "" || invalid || unchanged;

  const handleClose = () => {
    setNewUsername("");
    onClose();
  };

  const handleSubmit = async () => {
    if (okDisabled) return;
    setIsLoading(true);
    try {
      await apiClient.post(`/admin/users/${encodeURIComponent(userId)}/rename`, {
        new_username: trimmed,
      });
      feedback.message.success(
        `Renamed "${currentUsername ?? userLabel}" → "${trimmed}". Their sites re-render within about a minute.`,
      );
      qc.invalidateQueries({ queryKey: ["list", "users"] });
      handleClose();
    } catch (err: unknown) {
      // 403 stepup_required is handled globally (apiClient redirects to re-auth).
      const errMsg =
        (err as { response?: { data?: { detail?: string; error?: string } } })
          ?.response?.data?.detail ??
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err instanceof Error ? err.message : "Rename failed");
      feedback.message.error(errMsg);
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <Modal
      title="Rename user"
      open={open}
      onCancel={handleClose}
      onOk={handleSubmit}
      okText="Rename"
      okButtonProps={{ danger: true, disabled: okDisabled }}
      confirmLoading={isLoading}
      destroyOnHidden
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        Renames the Linux account and login for <b>{currentUsername ?? userLabel}</b> in
        place — same uid, so files aren't re-chowned. The home directory moves
        (<code>/home/{currentUsername ?? "…"}</code> → <code>/home/{trimmed || "…"}</code>)
        and their sites re-render under the new path.
      </Typography.Paragraph>

      <Form layout="vertical">
        <Form.Item
          label="New username"
          validateStatus={trimmed !== "" && (invalid || unchanged) ? "error" : undefined}
          help={
            trimmed !== "" && invalid
              ? "Must match ^[a-z_][a-z0-9_-]{0,31}$ (lowercase, starts with a letter or _)."
              : trimmed !== "" && unchanged
                ? "That's already the current username."
                : undefined
          }
        >
          <Input
            autoFocus
            value={newUsername}
            placeholder="e.g. acme"
            onChange={(e) => setNewUsername(e.target.value)}
            onPressEnter={handleSubmit}
          />
        </Form.Item>
      </Form>

      <Alert
        type="warning"
        showIcon
        message="Before you rename"
        description={
          <ul style={{ margin: 0, paddingInlineStart: 18 }}>
            <li>
              Refused (v1) if the user has <b>FTP/SFTP subaccounts</b> or{" "}
              <b>Python apps</b> — remove those first.
            </li>
            <li>
              Their <b>MariaDB databases and DB users are re-prefixed</b> to the new
              name. <b>You must update any app config</b> (e.g. wp-config.php) that
              references the old database name or user — the panel can't rewrite app
              files. Mail is unaffected.
            </li>
            <li>
              Refused if the user has any <b>PostgreSQL</b> database or role
              (re-prefixing those isn't supported yet).
            </li>
            <li>You'll be asked to re-authenticate the first time in a session.</li>
          </ul>
        }
      />
    </Modal>
  );
};

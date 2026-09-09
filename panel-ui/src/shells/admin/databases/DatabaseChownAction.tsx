// DatabaseChownAction — controlled modal that reassigns a database (and the DB
// users bound only to it) to a new tenant (GH #1609).
//
// The database and its exclusively-bound DB users are renamed onto the new
// owner's `<username>_` prefix (so the new owner's mysqladmin wildcard grant
// covers them and the tenant list shows them), their grants are re-pointed, and
// the panel rows are repointed. POST /admin/databases/:id/chown is admin-only
// AND behind the JAB-380 step-up — a stale session gets a 403 that apiClient
// turns into a re-auth + retry.
//
// v1 (MariaDB) refuses a postgres database, a database that backs an app install
// (its config holds the current owner's credentials → cross-tenant leak), a DB
// user shared with another database, and off-convention names. The backend
// returns those as a 4xx with a `detail` message, surfaced verbatim.
import { useMemo, useState } from "react";
import { Alert, Form, Modal, Select, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { useListQuery } from "../../../hooks/useQueries";
import type { Database } from "./DatabaseList";

// The subset of the /users list payload the picker needs.
type PickableUser = {
  id: string;
  username?: string | null;
  is_admin?: boolean;
  linux_uid?: number | null;
};

interface DatabaseChownActionProps {
  database: Database;
  open: boolean;
  onClose: () => void;
}

export const DatabaseChownAction = ({ database, open, onClose }: DatabaseChownActionProps) => {
  const qc = useQueryClient();
  const [isLoading, setIsLoading] = useState(false);
  const [newOwnerId, setNewOwnerId] = useState<string | undefined>(undefined);

  const isPostgres = database.engine === "postgres";

  // Fetch tenants for the picker. The backend is the real gate (it refuses a
  // non-tenant, an unprovisioned account, or the current owner); the client
  // filter just keeps obviously-invalid choices out of the list.
  const usersQ = useListQuery<PickableUser>({
    resource: "users",
    params: { pageSize: 200, is_admin: false },
    enabled: open && !isPostgres,
  });

  const options = useMemo(
    () =>
      usersQ.items
        .filter((u) => !u.is_admin && u.id !== database.user_id)
        .map((u) => ({
          value: u.id,
          disabled: u.linux_uid == null,
          label:
            (u.username ?? u.id) + (u.linux_uid == null ? " (not provisioned yet)" : ""),
        })),
    [usersQ.items, database.user_id],
  );

  const okDisabled = isPostgres || !newOwnerId;

  const handleClose = () => {
    setNewOwnerId(undefined);
    onClose();
  };

  const handleSubmit = async () => {
    if (!newOwnerId) return;
    setIsLoading(true);
    try {
      await apiClient.post(`/admin/databases/${encodeURIComponent(database.id)}/chown`, {
        new_owner_id: newOwnerId,
      });
      feedback.message.success(`Reassigned "${database.name}" to its new owner.`);
      qc.invalidateQueries({ queryKey: ["list", "databases"] });
      handleClose();
    } catch (err: unknown) {
      // 403 stepup_required is handled globally (apiClient redirects to re-auth).
      const errMsg =
        (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data
          ?.detail ??
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        (err instanceof Error ? err.message : "Change owner failed");
      feedback.message.error(errMsg);
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <Modal
      title="Change owner"
      open={open}
      onCancel={handleClose}
      onOk={handleSubmit}
      okText="Change owner"
      okButtonProps={{ danger: true, disabled: okDisabled }}
      confirmLoading={isLoading}
      destroyOnHidden
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        Reassigns <b>{database.name}</b> to another tenant. The database and the
        DB users bound only to it are renamed onto the new owner's prefix and
        their grants are re-pointed. Applications using the old name/credentials
        will need updating.
      </Typography.Paragraph>

      {isPostgres ? (
        <Alert
          type="info"
          showIcon
          message="PostgreSQL databases can't be reassigned yet"
          description="v1 supports MariaDB databases only. Reassigning a PostgreSQL database is a follow-up."
        />
      ) : (
        <>
          <Form layout="vertical">
            <Form.Item label="New owner">
              <Select
                autoFocus
                showSearch
                placeholder="Select a tenant"
                value={newOwnerId}
                loading={usersQ.isLoading}
                options={options}
                optionFilterProp="label"
                onChange={(v: string) => setNewOwnerId(v)}
              />
            </Form.Item>
          </Form>

          <Alert
            type="warning"
            showIcon
            message="Before you change the owner"
            description={
              <ul style={{ margin: 0, paddingInlineStart: 18 }}>
                <li>
                  Refused (v1) if the database backs an <b>app install</b> (e.g.
                  WordPress) — its config holds the current owner's credentials.
                  Detach or migrate the app first.
                </li>
                <li>
                  Refused if a DB user attached to this database also has access
                  to another database (it can't be moved cleanly).
                </li>
                <li>The database and its users are renamed onto the new owner's prefix.</li>
                <li>You'll be asked to re-authenticate the first time in a session.</li>
              </ul>
            }
          />
        </>
      )}
    </Modal>
  );
};

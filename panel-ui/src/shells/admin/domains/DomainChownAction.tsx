// DomainChownAction — controlled modal that reassigns a domain to a new tenant
// (GH #1238, WebUI for `jabali domain chown`).
//
// The domain_id is preserved, so the SSL lineage, DNS zone, tombstones and
// mailboxes stay; the docroot is moved into the new owner's home and re-owned
// to their uid, and the vhost re-renders on the reconciler's next tick.
// POST /admin/domains/:id/chown is admin-only AND behind the JAB-380 step-up —
// a stale session gets a 403 that apiClient turns into a re-auth + retry.
//
// v1 refuses a domain that has an app install (its config carries the current
// owner's DB credentials → cross-tenant leak). The backend returns that as a
// 422 with a `detail` message, which we surface verbatim.
import { useMemo, useState } from "react";
import { Alert, Form, Modal, Select, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { useListQuery } from "../../../hooks/useQueries";
import type { Domain } from "../../../components/domains/types";

// The subset of the /users list payload the picker needs.
type PickableUser = {
  id: string;
  username?: string | null;
  is_admin?: boolean;
  linux_uid?: number | null;
};

interface DomainChownActionProps {
  domain: Domain;
  open: boolean;
  onClose: () => void;
}

export const DomainChownAction = ({ domain, open, onClose }: DomainChownActionProps) => {
  const qc = useQueryClient();
  const [isLoading, setIsLoading] = useState(false);
  const [newOwnerId, setNewOwnerId] = useState<string | undefined>(undefined);

  // Fetch tenants for the picker. The backend is the real gate (it refuses a
  // non-tenant, an unprovisioned account, or the current owner); the client
  // filter is just to keep obviously-invalid choices out of the list.
  const usersQ = useListQuery<PickableUser>({
    resource: "users",
    // Scope the fetch to tenants server-side (the list handler honours
    // ?is_admin=false) so the page cap isn't spent on admin rows we'd filter
    // out anyway.
    params: { pageSize: 200, is_admin: false },
    enabled: open,
  });

  const options = useMemo(
    () =>
      usersQ.items
        .filter((u) => !u.is_admin && u.id !== domain.user_id)
        .map((u) => ({
          value: u.id,
          // An account without a linux_uid isn't fully provisioned yet — the
          // backend would 422 it, so disable it and say why.
          disabled: u.linux_uid == null,
          label:
            (u.username ?? u.id) + (u.linux_uid == null ? " (not provisioned yet)" : ""),
        })),
    [usersQ.items, domain.user_id],
  );

  const okDisabled = !newOwnerId;

  const handleClose = () => {
    setNewOwnerId(undefined);
    onClose();
  };

  const handleSubmit = async () => {
    if (!newOwnerId) return;
    setIsLoading(true);
    try {
      await apiClient.post(`/admin/domains/${encodeURIComponent(domain.id)}/chown`, {
        new_owner_id: newOwnerId,
      });
      feedback.message.success(
        `Reassigned "${domain.name}". Its site re-renders under the new owner within about a minute.`,
      );
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
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
        Reassigns <b>{domain.name}</b> to another tenant. The document root moves
        into the new owner's home and is re-owned to their user; the domain keeps
        its SSL, DNS and mail. The site re-renders under the new owner within
        about a minute.
      </Typography.Paragraph>

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
              Refused (v1) if the domain has an <b>app install</b> (e.g. WordPress) —
              its config holds the current owner's database credentials. Detach or
              migrate the app + its database to the new owner first.
            </li>
            <li>The files move to the new owner's home and are re-owned to their user.</li>
            <li>You'll be asked to re-authenticate the first time in a session.</li>
          </ul>
        }
      />
    </Modal>
  );
};

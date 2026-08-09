// AdminIPList — admin list of managed IPs (M24).
//
// Same shape as PackageList.tsx: useTableURL + SearchableTable +
// RowDeleteButton. Delete handles the 409 ip_in_use case by surfacing
// the affected-domains list returned by the API.
import { useTranslation } from "react-i18next";
import { Button, Card, Modal, Space, Table, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { DeleteOutlined, EditOutlined, EthernetPortOutlined } from "@icons";
import { RowActions } from "../../../components/RowActions";
import { useState } from "react";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { AdminIPDrawer } from "./AdminIPDrawer";

type ManagedIP = {
  id: number;
  address: string;
  family: "ipv4" | "ipv6";
  label: string;
  is_default: boolean;
  is_bound: boolean;
  is_user_selectable: boolean;
  degraded: boolean;
  // kernel_present is populated from agent ip.list when the agent is
  // reachable; omitted when the probe fails (UI falls back to the
  // is_bound-only view).
  kernel_present?: boolean;
  created_at: string;
  updated_at: string;
};

const renderBoundTag = (row: ManagedIP) => {
  const { is_bound, kernel_present } = row;
  if (kernel_present === undefined) {
    return is_bound ? <Tag color="green">bound</Tag> : <Tag>unbound</Tag>;
  }
  if (is_bound && kernel_present) return <Tag color="green">bound</Tag>;
  if (is_bound && !kernel_present) return <Tag color="red">lost</Tag>;
  if (!is_bound && kernel_present) return <Tag color="blue">system</Tag>;
  return <Tag>unbound</Tag>;
};

// affectedDomainsError is the body shape the API returns on 409
// ip_in_use. We surface the list in a Modal so the admin can copy
// names and reassign before retrying the delete.
type AffectedDomainsBody = {
  error?: string;
  detail?: string;
  affected_domains?: string[];
  affected_count?: number;
};

export const AdminIPList = () => {
  const { t } = useTranslation();
  const [conflictModal, setConflictModal] = useState<AffectedDomainsBody | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingId, setEditingId] = useState<number | undefined>(undefined);

  const openCreate = () => {
    setEditingId(undefined);
    setDrawerOpen(true);
  };
  const openEdit = (id: number) => {
    setEditingId(id);
    setDrawerOpen(true);
  };
  const closeDrawer = () => setDrawerOpen(false);

  const query = useTableURL<ManagedIP>({
    resource: "admin/ips",
    defaultSort: "id",
    defaultOrder: "asc",
  });
  const deleteMutation = useDeleteMutation({ resource: "admin/ips" });

  const handleDelete = async (row: ManagedIP) => {
    try {
      await deleteMutation.mutateAsync({ id: String(row.id) });
      feedback.message.success(`Removed ${row.address} from the pool`);
    } catch (err: unknown) {
      // Conflict surfaces the affected-domains list in a Modal — admin
      // needs to reassign those domains before deleting.
      const body = (err as { body?: AffectedDomainsBody })?.body;
      if (body?.error === "ip_in_use") {
        setConflictModal(body);
        return;
      }
      feedback.message.error(err instanceof Error ? err.message : "Delete failed");
    }
  };

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
        }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <EthernetPortOutlined /> IP Addresses
        </Typography.Title>
        <Button type="primary" onClick={openCreate}>
          Add IP
        </Button>
      </Space>

      <Card>
        <SearchableTableStringQ<ManagedIP>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by address or label"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
            showSizeChanger: true,
            // GH #232: controlled pagination needs onChange or page/size
            // clicks are inert (current/pageSize are pinned to query state).
            onChange: (page, pageSize) => query.setParams({ page, pageSize }),
          }}
          locale={{
            emptyText: (
              <EmptyWithCTA
                description={t("adminiplist.no_managed_ips_yet")}
                ctaLabel="Add IP"
                onCta={openCreate}
              />
            ),
          }}
        >
          <Table.Column
            dataIndex="address"
            title={t("adminiplist.address")}
            key="address"
            sorter={(a: ManagedIP, b: ManagedIP) => a.address.localeCompare(b.address)}
            render={(addr: string) => <code>{addr}</code>}
          />
          <Table.Column
            dataIndex="family"
            title={t("adminiplist.family")}
            sorter={(a: ManagedIP, b: ManagedIP) => a.family.localeCompare(b.family)}
            render={(family: ManagedIP["family"]) => (
              <Tag color={family === "ipv4" ? "blue" : "purple"}>{family}</Tag>
            )}
          />
          <Table.Column
            dataIndex="label"
            title={t("adminiplist.label")}
            sorter={(a: ManagedIP, b: ManagedIP) => (a.label ?? "").localeCompare(b.label ?? "")}
          />
          <Table.Column
            dataIndex="is_default"
            title={t("adminiplist.default")}
            sorter={(a: ManagedIP, b: ManagedIP) => Number(a.is_default) - Number(b.is_default)}
            render={(v: boolean) => (v ? <Tag color="gold">default</Tag> : null)}
          />
          <Table.Column
            title={t("adminiplist.bound")}
            key="is_bound"
            sorter={(a: ManagedIP, b: ManagedIP) => Number(a.is_bound) - Number(b.is_bound)}
            render={(_: unknown, row: ManagedIP) => renderBoundTag(row)}
          />
          <Table.Column
            dataIndex="is_user_selectable"
            title={t("adminiplist.user_selectable")}
            sorter={(a: ManagedIP, b: ManagedIP) => Number(a.is_user_selectable) - Number(b.is_user_selectable)}
            render={(v: boolean) =>
              v ? <Tag color="cyan">yes</Tag> : <Tag>no</Tag>
            }
          />
          <Table.Column
            dataIndex="degraded"
            title={t("adminiplist.status")}
            sorter={(a: ManagedIP, b: ManagedIP) => Number(a.degraded) - Number(b.degraded)}
            render={(v: boolean) =>
              v ? <Tag color="red">degraded</Tag> : <Tag color="green">ok</Tag>
            }
          />
          <Table.Column
            title={t("adminiplist.actions")}
            dataIndex="actions"
            render={(_: unknown, r: ManagedIP) => (
              <RowActions
                actions={[
                  { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => openEdit(r.id) },
                  { key: "delete", label: "Delete", icon: <DeleteOutlined />, danger: true, loading: deleteMutation.isPending, onClick: () => handleDelete(r) },
                ]}
              />
            )}
          />
        </SearchableTableStringQ>
      </Card>

      <AdminIPDrawer open={drawerOpen} onClose={closeDrawer} editingId={editingId} />

      <Modal
        title={t("adminiplist.ip_is_in_use")}
        open={conflictModal !== null}
        onCancel={() => setConflictModal(null)}
        onOk={() => setConflictModal(null)}
        cancelButtonProps={{ style: { display: "none" } }}
      >
        <p>
          {conflictModal?.detail ??
            "This IP is bound to one or more domains. Reassign them before deleting."}
        </p>
        {conflictModal?.affected_count ? (
          <p>
            <strong>{conflictModal.affected_count}</strong> domain
            {conflictModal.affected_count === 1 ? "" : "s"} reference this IP:
          </p>
        ) : null}
        {conflictModal?.affected_domains?.length ? (
          <ul style={{ maxHeight: 200, overflowY: "auto" }}>
            {conflictModal.affected_domains.map((d) => (
              <li key={d}>{d}</li>
            ))}
          </ul>
        ) : null}
      </Modal>
    </div>
  );
};

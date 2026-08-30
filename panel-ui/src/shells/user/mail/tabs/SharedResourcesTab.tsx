// SharedResourcesTab — M52 (ADR-0133). Manage standalone shared calendars,
// address books, and file folders across the user's mail-enabled domains, and
// assign them to mailboxes / groups via grants. The shared-mailbox kind (#241)
// is intentionally not offered here yet — its send-as path is pending.
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { Button, Flex, Empty, Form, Input, Modal, Segmented, Select, Skeleton, Space, Tag, Typography } from "antd";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts
import { useQueries, useQuery } from "@tanstack/react-query";
import { DeleteOutlined, PlusOutlined, TeamOutlined } from "@icons";

import { apiClient } from "../../../../apiClient";
import { useListQuery } from "../../../../hooks/useQueries";
import { SearchableTableStringQ } from "../../../../components/SearchableTable";
import { RowActions } from "../../../../components/RowActions";
import type { Domain } from "../../domains/UserDomainList";
import type { Mailbox } from "../../../../hooks/useMailboxes";
import {
  fetchSharedResourceDetail,
  useCreateSharedResource,
  useDeleteSharedResource,
  useSetSharedResourceGrants,
  type GrantLevel,
  type SharedResource,
  type SharedResourceKind,
} from "../../../../hooks/useSharedResources";

type Row = SharedResource & { domain_name: string };

const KIND_LABEL: Record<SharedResourceKind, string> = {
  calendar: "Calendar",
  addressbook: "Contacts",
  files: "Files",
  mailbox: "Mailbox",
};
const KIND_COLOR: Record<string, string> = {
  calendar: "green",
  addressbook: "purple",
  files: "gold",
  mailbox: "blue",
};
// Kinds offered in the UI (shared mailbox deferred — send-as path pending).
const OFFERED_KINDS: SharedResourceKind[] = ["calendar", "addressbook", "files"];

export const SharedResourcesTab = ({ domainId }: { domainId?: string } = {}) => {
  const { t } = useTranslation();
  const { items: domains, isLoading: loadingDomains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });
  const mailDomains = useMemo(() => domains.filter((d) => d.email_enabled && (!domainId || d.id === domainId)), [domains, domainId]);

  const results = useQueries({
    queries: mailDomains.map((d) => ({
      queryKey: ["shared_resources", d.id],
      queryFn: async () => {
        const { data } = await apiClient.get<{ data: SharedResource[] }>(
          `/domains/${d.id}/shared-resources`,
        );
        return (data.data ?? []).map((r) => ({ ...r, domain_name: d.name }));
      },
    })),
  });
  const anyLoading = results.some((r) => r.isLoading);
  const rows: Row[] = useMemo(() => results.flatMap((r) => r.data ?? []), [results]);
  const [search, setSearch] = useState("");
  const filteredRows = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter(
      (r) =>
        (r.display_name ?? "").toLowerCase().includes(q) ||
        (r.email ?? "").toLowerCase().includes(q) ||
        (r.domain_name ?? "").toLowerCase().includes(q),
    );
  }, [rows, search]);

  const [createOpen, setCreateOpen] = useState(false);
  const [grantsTarget, setGrantsTarget] = useState<Row | null>(null);
  const deleteMut = useDeleteSharedResource();

  if (loadingDomains && domains.length === 0) return <Skeleton active paragraph={{ rows: 4 }} />;
  if (mailDomains.length === 0)
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("sharedresourcestab.no_email_enabled_domains_yet")} />;

  return (
    <>
      <Flex wrap align="center" justify="space-between" gap="small" style={{ width: "100%", marginBottom: 12 }}>
        <Typography.Title level={3} style={{ margin: 0 }}>
          Shared Resources
        </Typography.Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
          New shared resource
        </Button>
      </Flex>

      <SearchableTableStringQ<Row>
        rowKey="id"
        loading={anyLoading && rows.length === 0}
        dataSource={filteredRows}
        searchPlaceholder="Search name, email, domain…"
        initialSearch={search}
        onSearchChange={setSearch}
        pagination={false}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Type",
            dataIndex: "kind",
            sorter: (a: Row, b: Row) => a.kind.localeCompare(b.kind),
            width: 120,
            render: (k: SharedResourceKind) => <Tag color={KIND_COLOR[k]}>{KIND_LABEL[k]}</Tag>,
          },
          {
            title: "Name",
            dataIndex: "display_name",
            sorter: (a: Row, b: Row) => (a.display_name || a.email || a.id).localeCompare(b.display_name || b.email || b.id),
            render: (v, r) => v || r.email || r.id,
          },
          ...(domainId
            ? []
            : [
                {
                  title: "Domain",
                  dataIndex: "domain_name",
                  sorter: (a: Row, b: Row) => (a.domain_name ?? "").localeCompare(b.domain_name ?? ""),
                },
              ]),
          {
            title: "Address",
            dataIndex: "email",
            sorter: (a: Row, b: Row) => (a.email ?? "").localeCompare(b.email ?? ""),
            render: (v: string | null) =>
              v ? <Typography.Text style={{ fontFamily: "monospace" }}>{v}</Typography.Text> : "—",
          },
          {
            title: "Actions",
            width: 160,
            render: (_, row) => (
              <RowActions
                actions={[
                  { key: "grants", label: "Grants", icon: <TeamOutlined />, onClick: () => setGrantsTarget(row) },
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: async () => {
                      try {
                        await deleteMut.mutateAsync({ id: row.id });
                        feedback.message.success("Deleted");
                      } catch {
                        feedback.message.error("Delete failed");
                      }
                    },
                    confirm: { title: `Delete ${row.display_name || row.email}?`, description: "Removes the shared collection and all its grants.", okText: "Delete" },
                  },
                ]}
              />
            ),
          },
        ]}
      />

      {createOpen && (
        <CreateModal
          domains={mailDomains.map((d) => ({ id: d.id, name: d.name }))}
          onClose={() => setCreateOpen(false)}
        />
      )}
      {grantsTarget && (
        <GrantsModal resource={grantsTarget} onClose={() => setGrantsTarget(null)} />
      )}
    </>
  );
};

function CreateModal({
  domains,
  onClose,
}: {
  domains: { id: string; name: string }[];
  onClose: () => void;
}) {
  const [form] = Form.useForm<{ domainId: string; kind: SharedResourceKind; name: string; display_name: string }>();
  const createMut = useCreateSharedResource();
  const submit = async () => {
    const v = await form.validateFields();
    try {
      await createMut.mutateAsync(v);
      feedback.message.success("Shared resource created");
      onClose();
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        "Create failed";
      feedback.message.error(msg);
    }
  };
  return (
    <Modal open title="New shared resource" onCancel={onClose} onOk={submit} okText="Create" confirmLoading={createMut.isPending} destroyOnClose>
      <Form form={form} layout="vertical" preserve={false} initialValues={{ kind: "calendar" }}>
        <Form.Item name="domainId" label="Domain" rules={[{ required: true, message: "Select a domain" }]}>
          <Select placeholder="Select domain" options={domains.map((d) => ({ label: d.name, value: d.id }))} />
        </Form.Item>
        <Form.Item name="kind" label="Type" rules={[{ required: true }]}>
          <Segmented options={OFFERED_KINDS.map((k) => ({ label: KIND_LABEL[k], value: k }))} />
        </Form.Item>
        <Form.Item name="display_name" label="Name" rules={[{ required: true, message: "Enter a name" }]}>
          <Input placeholder="Team Calendar" />
        </Form.Item>
        <Form.Item
          name="name"
          label="Address local part"
          extra="Internal host address; not a mail recipient for calendars/contacts/files."
          rules={[{ required: true, message: "Enter a local part" }, { pattern: /^[a-z0-9._-]+$/, message: "lowercase letters, digits, . _ -" }]}
        >
          <Input addonAfter="@domain" placeholder="team-cal" />
        </Form.Item>
      </Form>
    </Modal>
  );
}

function GrantsModal({ resource, onClose }: { resource: Row; onClose: () => void }) {
  const setGrants = useSetSharedResourceGrants();
  const [rights, setRights] = useState<GrantLevel>("readwrite");
  const [mailboxIds, setMailboxIds] = useState<string[]>([]);
  const [groupIds, setGroupIds] = useState<string[]>([]);

  // Current grants (seed the selections).
  const { isLoading: loadingDetail } = useQuery({
    queryKey: ["shared_resource_detail", resource.id],
    queryFn: async () => {
      const d = await fetchSharedResourceDetail(resource.id);
      setMailboxIds(d.grants.filter((g) => g.grantee_kind === "mailbox").map((g) => g.grantee_id));
      setGroupIds(d.grants.filter((g) => g.grantee_kind === "group").map((g) => g.grantee_id));
      const first = d.grants[0]?.rights;
      if (first) setRights(first);
      return d;
    },
  });

  // Mailboxes + groups in this resource's domain (grantee options).
  const { data: mailboxes = [] } = useQuery({
    queryKey: ["mb-for-grants", resource.domain_id],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: Mailbox[] }>(
        `/domains/${resource.domain_id}/mailboxes?page=1&page_size=200&sort=local_part&order=asc`,
      );
      return data.data ?? [];
    },
  });
  const { data: groups = [] } = useQuery({
    queryKey: ["grp-for-grants", resource.domain_id],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: { id: string; display_name: string; email: string }[] }>(
        `/domains/${resource.domain_id}/mailgroups`,
      );
      return data.data ?? [];
    },
  });

  const save = async () => {
    const grants = [
      ...mailboxIds.map((id) => ({ grantee_kind: "mailbox" as const, grantee_id: id, rights })),
      ...groupIds.map((id) => ({ grantee_kind: "group" as const, grantee_id: id, rights })),
    ];
    try {
      await setGrants.mutateAsync({ id: resource.id, grants });
      feedback.message.success("Grants saved — apply on next sync");
      onClose();
    } catch {
      feedback.message.error("Save failed");
    }
  };

  return (
    <Modal open title={`Grants — ${resource.display_name || resource.email}`} onCancel={onClose} onOk={save} okText="Save" confirmLoading={setGrants.isPending} destroyOnClose>
      {loadingDetail ? (
        <Skeleton active paragraph={{ rows: 3 }} />
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <div>
            <Typography.Text strong>Access level</Typography.Text>
            <Segmented
              block
              style={{ display: "block", marginTop: 4 }}
              value={rights}
              onChange={(v) => setRights(v as "read" | "readwrite" | "admin")}
              options={[
                { label: "Read", value: "read" },
                { label: "Read/Write", value: "readwrite" },
                { label: "Admin", value: "admin" },
              ]}
            />
          </div>
          <div>
            <Typography.Text strong>Mailboxes</Typography.Text>
            <Select
              mode="multiple"
              allowClear
              style={{ width: "100%", marginTop: 4 }}
              placeholder="Grant to mailboxes"
              value={mailboxIds}
              onChange={setMailboxIds}
              optionFilterProp="label"
              options={mailboxes.map((m) => ({ label: m.email, value: m.id }))}
            />
          </div>
          <div>
            <Typography.Text strong>Groups</Typography.Text>
            <Select
              mode="multiple"
              allowClear
              style={{ width: "100%", marginTop: 4 }}
              placeholder="Grant to groups (expands to members)"
              value={groupIds}
              onChange={setGroupIds}
              optionFilterProp="label"
              options={groups.map((g) => ({ label: g.display_name || g.email, value: g.id }))}
            />
          </div>
          <Typography.Text type="secondary">
            One access level applies to all grantees here. The same collection can be shared with
            many mailboxes/groups.
          </Typography.Text>
        </Space>
      )}
    </Modal>
  );
}

// AdminGroupsTab — server-wide mail groups (M51, issue #201 admin view).
// Lists every group across all domains; reuses the user GroupDrawer +
// MembersModal for edit/members. Create uses a "Create in:" domain picker
// since groups are domain-scoped.
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { RowActions } from "../../../components/RowActions";
import {
  App,
  Button,
  Empty,
  Select,
  Skeleton,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { DeleteOutlined, EditOutlined, PlusOutlined, UsergroupAddOutlined } from "@icons";

import { useListQuery } from "../../../hooks/useQueries";
import type { Domain } from "../../user/domains/UserDomainList";
import {
  useAdminMailGroups,
  useDeleteMailGroup,
  type AdminMailGroup,
  type MailGroup,
} from "../../../hooks/useMailGroups";
import { GroupDrawer, MembersModal } from "../../user/mail/tabs/GroupsTab";

const RESOURCE_TAGS: { key: keyof MailGroup; label: string; color: string }[] = [
  { key: "has_mailbox", label: "Mailbox", color: "blue" },
  { key: "has_calendar", label: "Calendar", color: "green" },
  { key: "has_addressbook", label: "Contacts", color: "purple" },
  { key: "has_files", label: "Files", color: "gold" },
];

export function AdminGroupsTab() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const { data: rows, isLoading } = useAdminMailGroups();
  const { items: domains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 500, sort: "name", order: "asc" },
  });
  const mailDomains = useMemo(() => domains.filter((d) => d.email_enabled), [domains]);

  const [createDomain, setCreateDomain] = useState<string | undefined>(undefined);
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminMailGroup | null>(null);
  const [membersTarget, setMembersTarget] = useState<AdminMailGroup | null>(null);

  const del = useDeleteMailGroup();
  const effectiveCreateDomain = createDomain ?? mailDomains[0]?.id;

  if (isLoading && !rows) return <Skeleton active paragraph={{ rows: 6 }} />;

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Space style={{ width: "100%", justifyContent: "flex-end" }} wrap>
        <Typography.Text type="secondary">Create in:</Typography.Text>
        <Select
          style={{ minWidth: 220 }}
          value={effectiveCreateDomain}
          onChange={setCreateDomain}
          options={mailDomains.map((d) => ({ value: d.id, label: d.name }))}
          showSearch
          optionFilterProp="label"
          disabled={mailDomains.length === 0}
        />
        <Button
          type="primary"
          icon={<PlusOutlined />}
          disabled={mailDomains.length === 0}
          onClick={() => setCreateOpen(true)}
        >
          New group
        </Button>
      </Space>

      <Table<AdminMailGroup>
        scroll={{ x: "max-content" }}
        rowKey="id"
        dataSource={rows ?? []}
        pagination={{ defaultPageSize: 25, showSizeChanger: true }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("admingroupstab.no_groups")} /> }}
        columns={[
          {
            title: "Group",
            dataIndex: "email",
            render: (email: string, row) => (
              <Space direction="vertical" size={0}>
                <Typography.Text strong>{row.display_name || row.local_part}</Typography.Text>
                <Typography.Text type="secondary" style={{ fontFamily: "monospace", fontSize: 12 }}>
                  {email}
                </Typography.Text>
              </Space>
            ),
          },
          { title: "Domain", dataIndex: "domain_name" },
          {
            title: "Owner",
            dataIndex: "user_username",
            render: (v: string) => v || <Typography.Text type="secondary">—</Typography.Text>,
          },
          {
            title: "Resources",
            key: "resources",
            render: (_, row) => (
              <Space size={4} wrap>
                {RESOURCE_TAGS.filter((r) => row[r.key]).map((r) => (
                  <Tag key={r.label} color={r.color}>
                    {r.label}
                  </Tag>
                ))}
              </Space>
            ),
          },
          { title: "Members", dataIndex: "member_count", width: 100, render: (n: number) => <Tag>{n}</Tag> },
          {
            title: "Actions",
            key: "actions",
            render: (_, row) => (
              <RowActions
                actions={[
                  { key: "members", label: "Manage members", icon: <UsergroupAddOutlined />, onClick: () => setMembersTarget(row) },
                  { key: "edit", label: "Edit group", icon: <EditOutlined />, onClick: () => setEditTarget(row) },
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: async () => {
                      try {
                        await del.mutateAsync({ id: row.id, domainId: row.domain_id });
                        message.success("Group deleted");
                      } catch {
                        message.error("Failed to delete group");
                      }
                    },
                    confirm: { title: `Delete ${row.email}?`, description: "The shared mailbox, calendar, address book and files are removed. This cannot be undone.", okText: "Delete" },
                  },
                ]}
              />
            ),
          },
        ]}
      />

      <GroupDrawer
        open={createOpen}
        domainId={effectiveCreateDomain}
        group={null}
        onClose={() => setCreateOpen(false)}
      />
      <GroupDrawer
        open={editTarget !== null}
        domainId={editTarget?.domain_id}
        group={editTarget}
        onClose={() => setEditTarget(null)}
      />
      {membersTarget && <MembersModal group={membersTarget} onClose={() => setMembersTarget(null)} />}
    </Space>
  );
}

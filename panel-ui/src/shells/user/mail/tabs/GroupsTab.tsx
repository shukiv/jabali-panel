// GroupsTab — M51 mail groups (issue #201). Pick a mail-enabled domain,
// create/edit groups (shared mailbox + calendar + address book + files),
// manage membership, delete. Mirrors MailboxesTab styling.
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { RowActions } from "../../../../components/RowActions";
import {
  App,
  Button,
  Drawer,
  Empty,
  Form,
  Input,
  Modal,
  Select,
  Skeleton,
  Space,
  Switch,
  Tag,
  Segmented,
  Typography,
} from "antd";
import {
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  TeamOutlined,
  UsergroupAddOutlined,
} from "@icons";

import { useListQuery } from "../../../../hooks/useQueries";
import { SearchableTableStringQ } from "../../../../components/SearchableTable";
import type { Domain } from "../../domains/UserDomainList";
import { useMailboxes } from "../../../../hooks/useMailboxes";
import {
  useMailGroups,
  useMailGroup,
  useCreateMailGroup,
  useUpdateMailGroup,
  useDeleteMailGroup,
  useSetMailGroupMembers,
  type MailGroup,
} from "../../../../hooks/useMailGroups";


export function GroupsTab({ domainId: scopedDomainId }: { domainId?: string } = {}) {
  const { t } = useTranslation();
  const { message } = App.useApp();

  const { items: domains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });
  const mailDomains = useMemo(() => domains.filter((d) => d.email_enabled && (!scopedDomainId || d.id === scopedDomainId)), [domains, scopedDomainId]);

  // GH #1387: in the per-domain drill-down (scopedDomainId set) the domain is
  // fixed and the picker is hidden; otherwise the internal picker drives it.
  const [domainId, setDomainId] = useState<string | undefined>(undefined);
  const effectiveDomain = scopedDomainId ?? domainId ?? mailDomains[0]?.id;

  const { data: groups, isLoading } = useMailGroups(effectiveDomain);

  const [editTarget, setEditTarget] = useState<MailGroup | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [membersTarget, setMembersTarget] = useState<MailGroup | null>(null);

  const del = useDeleteMailGroup();

  if (mailDomains.length === 0) {
    return (
      <Empty
        image={Empty.PRESENTED_IMAGE_SIMPLE}
        description={t("groupstab.enable_email_on_a_domain_first_to_create_gro")}
      />
    );
  }

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Space style={{ width: "100%", justifyContent: "space-between" }} wrap>
        {scopedDomainId ? (
          <span />
        ) : (
          <Select
            style={{ minWidth: 240 }}
            value={effectiveDomain}
            onChange={setDomainId}
            options={mailDomains.map((d) => ({ value: d.id, label: d.name }))}
            showSearch
            optionFilterProp="label"
          />
        )}
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
          New group
        </Button>
      </Space>

      {isLoading && !groups ? (
        <Skeleton active paragraph={{ rows: 4 }} />
      ) : (
        <SearchableTableStringQ<MailGroup>
          scroll={{ x: "max-content" }}
          rowKey="id"
          dataSource={(groups ?? []).filter((g) => {
            const q = search.trim().toLowerCase();
            if (!q) return true;
            return (
              (g.email ?? "").toLowerCase().includes(q) ||
              (g.display_name ?? "").toLowerCase().includes(q) ||
              (g.local_part ?? "").toLowerCase().includes(q)
            );
          })}
          searchPlaceholder="Search group name…"
          initialSearch={search}
          onSearchChange={setSearch}
          pagination={{ defaultPageSize: 25 }}
          locale={{
            emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("groupstab.no_groups")} />,
          }}
          columns={[
            {
              title: "Group",
              dataIndex: "email",
              sorter: (a, b) => a.email.localeCompare(b.email),
              defaultSortOrder: "ascend" as const,
              render: (email: string, row) => (
                <Space direction="vertical" size={0}>
                  <Typography.Text strong>{row.display_name || row.local_part}</Typography.Text>
                  <Typography.Text type="secondary" style={{ fontFamily: "monospace", fontSize: 12 }}>
                    {email}
                  </Typography.Text>
                </Space>
              ),
            },
            {
              title: "Type",
              key: "group_kind",
              width: 150,
              render: (_, row) =>
                row.group_kind === "distribution" ? (
                  <Tag color="cyan">Distribution list</Tag>
                ) : (
                  <Tag color="geekblue">Shared workspace</Tag>
                ),
            },
            {
              title: "Members",
              dataIndex: "member_count",
              sorter: (a, b) => (a.member_count ?? 0) - (b.member_count ?? 0),
              width: 110,
              render: (n: number) => <Tag>{n}</Tag>,
            },
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
      )}

      <GroupDrawer
        open={createOpen}
        domainId={effectiveDomain}
        group={null}
        onClose={() => setCreateOpen(false)}
      />
      <GroupDrawer
        open={editTarget !== null}
        domainId={effectiveDomain}
        group={editTarget}
        onClose={() => setEditTarget(null)}
      />
      {membersTarget && (
        <MembersModal
          group={membersTarget}
          onClose={() => setMembersTarget(null)}
        />
      )}
    </Space>
  );
}

// --- Create / edit drawer ---------------------------------------------

export function GroupDrawer({
  open,
  domainId,
  group,
  onClose,
}: {
  open: boolean;
  domainId: string | undefined;
  group: MailGroup | null;
  onClose: () => void;
}) {
  const { message } = App.useApp();
  const [form] = Form.useForm();
  const create = useCreateMailGroup();
  const update = useUpdateMailGroup();
  const editing = group !== null;

  // Reset the form whenever the drawer opens for a different target.
  useMemo(() => {
    if (open) {
      form.setFieldsValue(
        group
          ? {
              name: group.local_part,
              display_name: group.display_name,
              group_kind: group.group_kind,
              description: group.description,
              internal_only: group.internal_only,
            }
          : {
              name: "",
              display_name: "",
              group_kind: "resource",
              description: "",
              internal_only: false,
            },
      );
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, group]);

  const submit = async () => {
    const v = await form.validateFields();
    try {
      if (editing && group) {
        await update.mutateAsync({
          id: group.id,
          domainId: group.domain_id,
          input: {
            display_name: v.display_name,
            description: v.description,
            internal_only: v.internal_only,
          },
        });
        message.success("Group updated");
      } else {
        if (!domainId) return;
        await create.mutateAsync({
          domainId,
          input: {
            name: v.name,
            display_name: v.display_name,
            description: v.description,
            group_kind: v.group_kind,
            internal_only: v.internal_only,
          },
        });
        message.success("Group created");
      }
      onClose();
    } catch (err) {
      const detail = (err as { response?: { data?: { detail?: string; error?: string } } })?.response
        ?.data;
      message.error(detail?.detail ?? detail?.error ?? "Failed to save group");
    }
  };

  return (
    <Drawer
      open={open}
      title={editing ? `Edit ${group?.email}` : "New group"}
      width={420}
      onClose={onClose}
      destroyOnClose
      extra={
        <Button type="primary" onClick={submit} loading={create.isPending || update.isPending}>
          Save
        </Button>
      }
    >
      <Form form={form} layout="vertical" requiredMark={false}>
        <Form.Item
          label="Group name"
          name="name"
          tooltip="The group address local part — group email is name@domain."
          rules={editing ? [] : [{ required: true, message: "Name is required" }]}
        >
          <Input placeholder="marketing" disabled={editing} autoComplete="off" />
        </Form.Item>
        <Form.Item label="Display name" name="display_name">
          <Input placeholder="Marketing" autoComplete="off" />
        </Form.Item>
        <Form.Item label="Type" name="group_kind" tooltip="Distribution list = a mailing list (mail to the address reaches every member). Shared workspace = members also share the group's calendar, contacts and files.">
          <Segmented
            disabled={editing}
            options={[
              { label: "Shared workspace", value: "resource" },
              { label: "Distribution list", value: "distribution" },
            ]}
          />
        </Form.Item>
        <Form.Item label="Description" name="description">
          <Input.TextArea placeholder="Optional note" rows={2} maxLength={255} />
        </Form.Item>
        <Form.Item
          label="Internal delivery only"
          name="internal_only"
          valuePropName="checked"
          tooltip="When on, the group address accepts mail only from senders in its own domain; external senders are rejected (GH #348)."
        >
          <Switch />
        </Form.Item>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          <b>Shared workspace</b>: members also share the group's calendar, contacts and
          files in webmail. <b>Distribution list</b>: mail to the address simply reaches
          every member (a mailing list). For a single calendar, address book or file
          folder shared with specific people, use the <b>Shared Resources</b> tab.
        </Typography.Text>
      </Form>
    </Drawer>
  );
}

// --- Members modal -----------------------------------------------------

export function MembersModal({ group, onClose }: { group: MailGroup; onClose: () => void }) {
  const { message } = App.useApp();
  const { data: detail, isLoading } = useMailGroup(group.id);
  const { items: mailboxes } = useMailboxes({
    domainId: group.domain_id,
    params: { page: 1, pageSize: 500 },
  });
  const setMembers = useSetMailGroupMembers();

  const [selected, setSelected] = useState<string[] | null>(null);
  const value = selected ?? detail?.members.map((m) => m.mailbox_id) ?? [];

  const submit = async () => {
    try {
      await setMembers.mutateAsync({
        id: group.id,
        domainId: group.domain_id,
        mailbox_ids: value,
      });
      message.success("Members updated");
      onClose();
    } catch {
      message.error("Failed to update members");
    }
  };

  return (
    <Modal
      open
      title={
        <Space>
          <TeamOutlined />
          {`Members — ${group.email}`}
        </Space>
      }
      okText="Save"
      confirmLoading={setMembers.isPending}
      onOk={submit}
      onCancel={onClose}
      destroyOnClose
    >
      {isLoading ? (
        <Skeleton active paragraph={{ rows: 3 }} />
      ) : (
        <Select
          mode="multiple"
          style={{ width: "100%" }}
          placeholder="Select member mailboxes"
          value={value}
          onChange={setSelected}
          optionFilterProp="label"
          options={mailboxes.map((m) => ({ value: m.id, label: m.email }))}
        />
      )}
    </Modal>
  );
}

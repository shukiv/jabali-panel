// GroupsTab — M51 mail groups (issue #201). Pick a mail-enabled domain,
// create/edit groups (shared mailbox + calendar + address book + files),
// manage membership, delete. Mirrors MailboxesTab styling.
//
// GH #1833: the Mail domain page shows each kind on its own tab (kind prop):
// "Distribution lists" next to Forwarders, and "Groups" for Shared
// Workspaces. A distribution list also delivers to outside addresses.
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { RowActions } from "../../../../components/RowActions";
import {
  Alert,
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
import type { Domain } from "../../../../components/domains/types";
import { useMailboxes } from "../../../../hooks/useMailboxes";
import {
  useMailGroups,
  useMailGroup,
  useCreateMailGroup,
  useUpdateMailGroup,
  useDeleteMailGroup,
  useSetMailGroupMembers,
  useSetMailGroupExternalMembers,
  MAX_REDIRECT_RECIPIENTS,
  type MailGroup,
} from "../../../../hooks/useMailGroups";

export type GroupKind = "distribution" | "resource";

const KIND_TEXT: Record<GroupKind, { create: string; search: string; empty: string; deleted: string; deleteDetail: string }> = {
  distribution: {
    create: "New distribution list",
    search: "Search list name…",
    empty: "No distribution lists",
    deleted: "Distribution list deleted",
    deleteDetail: "Mail to the address is no longer delivered. Members keep the mail they already received. This cannot be undone.",
  },
  resource: {
    create: "New group",
    search: "Search group name…",
    empty: "No groups",
    deleted: "Group deleted",
    deleteDetail: "The shared mailbox, calendar, address book and files are removed. This cannot be undone.",
  },
};

// A rough shape check that marks a typo before the save; the panel API is the
// real check and names the address it refuses.
const LOOKS_LIKE_ADDRESS = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function GroupsTab({
  domainId: scopedDomainId,
  kind,
}: { domainId?: string; kind?: GroupKind } = {}) {
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

  const { data: allGroups, isLoading } = useMailGroups(effectiveDomain);
  const groups = useMemo(
    () => (kind && allGroups ? allGroups.filter((g) => (g.group_kind === "distribution" ? "distribution" : "resource") === kind) : allGroups),
    [allGroups, kind],
  );
  const text = KIND_TEXT[kind ?? "resource"];

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
          {text.create}
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
          searchPlaceholder={text.search}
          initialSearch={search}
          onSearchChange={setSearch}
          pagination={{ defaultPageSize: 25 }}
          locale={{
            emptyText: (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description={kind === "distribution" ? text.empty : t("groupstab.no_groups")}
              />
            ),
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
            // One kind per tab needs no Type column.
            ...(kind
              ? []
              : [
                  {
                    title: "Type",
                    key: "group_kind",
                    width: 150,
                    render: (_: unknown, row: MailGroup) =>
                      row.group_kind === "distribution" ? (
                        <Tag color="cyan">Distribution list</Tag>
                      ) : (
                        <Tag color="geekblue">Shared workspace</Tag>
                      ),
                  },
                ]),
            {
              title: "Members",
              dataIndex: "member_count",
              sorter: (a, b) => (a.member_count ?? 0) - (b.member_count ?? 0),
              width: 110,
              render: (n: number, row) => <MemberCountTags members={n} outside={row.external_member_count} />,
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
                          message.success(KIND_TEXT[row.group_kind === "distribution" ? "distribution" : "resource"].deleted);
                        } catch {
                          message.error("Failed to delete group");
                        }
                      },
                      confirm: {
                        title: `Delete ${row.email}?`,
                        description: KIND_TEXT[row.group_kind === "distribution" ? "distribution" : "resource"].deleteDetail,
                        okText: "Delete",
                      },
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
        kind={kind}
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

// MemberCountTags shows a group's member mailboxes and, for a distribution
// list, how many outside addresses it delivers to (GH #1833).
export function MemberCountTags({ members, outside }: { members: number; outside?: number }) {
  return (
    <Space size={4} wrap>
      <Tag>{members}</Tag>
      {(outside ?? 0) > 0 && <Tag color="purple">{`+${outside} outside`}</Tag>}
    </Space>
  );
}

// --- Create / edit drawer ---------------------------------------------

export function GroupDrawer({
  open,
  domainId,
  group,
  kind,
  onClose,
}: {
  open: boolean;
  domainId: string | undefined;
  group: MailGroup | null;
  /** Fixes the type of a new group and hides the picker (GH #1833). */
  kind?: GroupKind;
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
              group_kind: kind ?? "distribution",
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
      title={editing ? `Edit ${group?.email}` : kind ? KIND_TEXT[kind].create : "New group"}
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
        <Form.Item
          label="Type"
          name="group_kind"
          hidden={kind !== undefined}
          tooltip="Distribution list: every member gets their own copy of each message in their own inbox. Shared workspace: one shared inbox that members open in webmail (no copies are delivered), plus a shared calendar, contacts and files. The type cannot be changed later."
        >
          <Segmented
            disabled={editing}
            options={[
              { label: "Distribution list", value: "distribution" },
              { label: "Shared workspace", value: "resource" },
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
          tooltip="When on, the group address accepts mail only from senders in its own domain; external senders are rejected (GH #348). A distribution list that is internal-only or has outside addresses can have at most 20 recipients, member mailboxes and outside addresses together; it still delivers to its outside addresses."
        >
          <Switch />
        </Form.Item>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          <b>Distribution list</b>: mail to the address is delivered to every member's own
          inbox (a mailing list). <b>Shared workspace</b>: mail goes to one shared inbox that
          members open in webmail, and members also share the group's calendar, contacts and
          files. For a single calendar, address book or file folder shared with specific
          people, use the <b>Shared Resources</b> tab.
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
  const setExternal = useSetMailGroupExternalMembers();
  const isList = group.group_kind === "distribution";

  const [selected, setSelected] = useState<string[] | null>(null);
  const value = selected ?? detail?.members.map((m) => m.mailbox_id) ?? [];

  // GH #1833: a distribution list's outside addresses (a Gmail address, say).
  const [outside, setOutside] = useState<string[] | null>(null);
  const savedOutside = detail?.external_members ?? [];
  const outsideValue = outside ?? savedOutside;
  const outsideCap = detail?.external_member_cap ?? 0;
  const badOutside = outsideValue.filter((a) => !LOOKS_LIKE_ADDRESS.test(a.trim()));
  const overCap = outsideValue.length > outsideCap && outsideValue.some((a) => !savedOutside.includes(a.trim().toLowerCase()));
  // A list that is internal-only or has outside addresses delivers by one
  // redirect per recipient, so mailboxes and outside addresses share a limit.
  const redirect = isList && ((detail?.internal_only ?? group.internal_only) || outsideValue.length > 0);
  const recipients = value.length + outsideValue.length;
  const overRecipients = redirect && recipients > MAX_REDIRECT_RECIPIENTS;
  const outsideCount =
    `${outsideValue.length} of ${outsideCap} outside addresses` +
    (redirect ? ` · ${recipients} of ${MAX_REDIRECT_RECIPIENTS} recipients in all` : "");

  const submit = async () => {
    if (badOutside.length > 0) {
      message.error(`Not an email address: ${badOutside.join(", ")}`);
      return;
    }
    const outsideChanged = isList && outside !== null;
    const saveMailboxes = () =>
      setMembers.mutateAsync({ id: group.id, domainId: group.domain_id, mailbox_ids: value });
    const saveOutside = () =>
      setExternal.mutateAsync({ id: group.id, domainId: group.domain_id, addresses: outsideValue });
    try {
      // A list that is internal-only or has outside addresses counts both
      // kinds toward one limit: save the set that shrinks first, so a swap
      // between them is not refused half-way.
      if (outsideChanged && outsideValue.length < savedOutside.length) {
        await saveOutside();
        await saveMailboxes();
      } else {
        await saveMailboxes();
        if (outsideChanged) await saveOutside();
      }
      message.success("Members updated");
      onClose();
    } catch (err) {
      const detail = (err as { response?: { data?: { detail?: string; error?: string } } })?.response
        ?.data;
      message.error(detail?.detail ?? detail?.error ?? "Failed to update members");
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
      confirmLoading={setMembers.isPending || setExternal.isPending}
      onOk={submit}
      onCancel={onClose}
      destroyOnClose
    >
      {isLoading ? (
        <Skeleton active paragraph={{ rows: 3 }} />
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <div>
            {isList && <Typography.Text strong>Member mailboxes</Typography.Text>}
            <Select
              mode="multiple"
              style={{ width: "100%" }}
              aria-label="Member mailboxes"
              placeholder="Select member mailboxes"
              value={value}
              onChange={setSelected}
              optionFilterProp="label"
              options={mailboxes.map((m) => ({ value: m.id, label: m.email }))}
            />
          </div>
          {isList && (
            <div>
              <Typography.Text strong>Outside addresses</Typography.Text>
              <Typography.Paragraph type="secondary" style={{ margin: "2px 0 6px", fontSize: 12 }}>
                Addresses on other mail servers, a Gmail address for example, that receive the
                list too. Type an address and press Enter.
              </Typography.Paragraph>
              {outsideCap === 0 && savedOutside.length === 0 ? (
                <Alert type="info" showIcon message="Your administrator does not allow outside addresses on lists." />
              ) : (
                <>
                  <Select
                    mode="tags"
                    style={{ width: "100%" }}
                    aria-label="Outside addresses"
                    placeholder="name@example.com"
                    value={outsideValue}
                    onChange={(v: string[]) => setOutside(v.map((a) => a.trim()).filter(Boolean))}
                    tokenSeparators={[",", " ", ";"]}
                    open={false}
                    suffixIcon={null}
                    status={badOutside.length > 0 || overCap ? "error" : undefined}
                  />
                  <Typography.Text type={overCap || overRecipients ? "danger" : "secondary"} style={{ fontSize: 12 }}>
                    {outsideCount}
                    {badOutside.length > 0 && ` · not an email address: ${badOutside.join(", ")}`}
                  </Typography.Text>
                  {outsideValue.length > 0 && (
                    <Alert
                      type="warning"
                      showIcon
                      style={{ marginTop: 8 }}
                      message="Mail from outside senders can land in spam"
                      description="The list sends each copy from its own address, so the receiving provider checks this server rather than the sender's. A message from someone outside your domain still shows that person as the sender, and Gmail, Outlook and other providers can file it as spam when the sender's domain asks them to be strict. A delivery failure comes back to the list and is dropped, not passed on to the members. With outside addresses the list can have at most 20 recipients, member mailboxes included."
                    />
                  )}
                </>
              )}
            </div>
          )}
        </Space>
      )}
    </Modal>
  );
}

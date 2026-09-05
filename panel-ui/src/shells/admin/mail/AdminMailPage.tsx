// AdminMailPage — server-wide mailbox management (admin). Lists every mailbox
// across all domains with add / edit / reset-password / delete. Reuses the
// per-domain create wizard + the shared EditMailboxModal (GH #197 companion).
//
// JAB-370: the directory is server-paginated / -searched / -sorted via
// useTableURL({ resource: "admin/mailboxes" }) + SearchableTableStringQ. The
// old client-side "load every mailbox then filter/sort/paginate in the browser"
// path did not scale past a few thousand mailboxes (one unbounded query + a
// large payload). Owner scope (?user_id, #483) is forwarded as an extra param.
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { useTabParam } from "../../../hooks/useTabParam";
import {
  App,
  Button,
  Empty,
  Form,
  Modal,
  Card,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import type { TableProps } from "antd";
import { DeleteOutlined, EditOutlined, KeyOutlined, MailOutlined, PlusOutlined } from "@icons";

import { useDeleteMailbox, type AdminMailbox } from "../../../hooks/useMailboxes";
import { useListQuery, useOneQuery } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { useSearchParams, Link } from "react-router";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { ownerResourceCrumbs, ownerLabel, adminLinks } from "../../../components/admin/entityLinks";
import type { Domain } from "../../../components/domains/types";
import { EditMailboxModal } from "../../../components/mail/EditMailboxModal";
import {
  MailboxPasswordRevealModal,
  renderMailboxQuota,
  renderMailboxStatus,
  useMailboxPasswordReset,
  useMailboxWebmail,
} from "../../../components/mail/mailboxInventory";
import { AdminGroupsTab } from "./AdminGroupsTab";
import { MailStatsTab } from "./MailStatsTab";
import { CreateMailboxWizardModal } from "../../user/mail/CreateMailboxWizardModal";
import { PasswordInput } from "../../../components/PasswordInput";
import { RowActions } from "../../../components/RowActions";

export function AdminMailPage() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [searchParams, setSearchParams] = useSearchParams();
  const ownerId = searchParams.get("user_id") ?? undefined;

  // Server-paginated directory. The owner filter is forwarded as an extra
  // param (not URL-backed by useTableURL — the URL's ?user_id is owned by the
  // owner-tag flow below), so switching owners re-keys the query and refetches.
  const query = useTableURL<AdminMailbox>({
    resource: "admin/mailboxes",
    defaultSort: "email",
    defaultOrder: "asc",
    defaultPageSize: 25,
    extraParams: ownerId ? { user_id: ownerId } : undefined,
  });

  const ownerQ = useOneQuery<{ id: string; username?: string | null }>({
    resource: "users",
    id: ownerId,
    enabled: !!ownerId,
  });
  const clearOwner = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("user_id");
    setSearchParams(next);
  };
  const { items: domains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 500, sort: "name", order: "asc" },
  });
  const emailDomains = useMemo(
    () => domains.filter((d) => d.email_enabled),
    [domains],
  );

  const deleteMutation = useDeleteMailbox();
  const { rotate: rotatePassword, rotateMutation, reveal, clearReveal, revealPassword } =
    useMailboxPasswordReset();
  const webmail = useMailboxWebmail();

  const [tab, setTab] = useTabParam<string>("mailboxes");
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminMailbox | null>(null);
  const [resetTarget, setResetTarget] = useState<AdminMailbox | null>(null);
  const [resetForm] = Form.useForm<{ password?: string }>();

  // Server-wide admin: a form modal collecting an OPTIONAL custom password. The
  // rotate → reveal-once → error core is the shared hook.
  const submitReset = async () => {
    if (!resetTarget) return;
    const v = await resetForm.validateFields();
    const ok = await rotatePassword({
      id: resetTarget.id,
      email: resetTarget.email,
      newPassword: v.password,
      title: t("adminmailpage.mailbox_password"),
    });
    if (ok) setResetTarget(null);
  };

  useSetBreadcrumbs(
    ownerId
      ? ownerResourceCrumbs({ id: ownerId, username: ownerQ.data?.username }, { key: "mailboxes", label: "Mailboxes" })
      : null,
  );

  const ownerRef = ownerId ? { id: ownerId, username: ownerQ.data?.username } : undefined;

  // Map AntD's per-column sort event onto the server sort/order params. Columns
  // set `sorter: true` (no local comparator) so sorting is authoritative across
  // ALL pages, not just the visible one; the repo whitelists the sort key.
  const sortOrderFor = (key: string): "ascend" | "descend" | null =>
    query.params.sort === key ? (query.params.order === "asc" ? "ascend" : "descend") : null;

  const handleTableChange: TableProps<AdminMailbox>["onChange"] = (pag, _filters, sorter, extra) => {
    if (extra.action === "sort") {
      const s = Array.isArray(sorter) ? sorter[0] : sorter;
      if (s?.order && s.columnKey) {
        query.setParams({ sort: String(s.columnKey), order: s.order === "ascend" ? "asc" : "desc", page: 1 });
      } else {
        query.setParams({ sort: undefined, order: undefined, page: 1 });
      }
    } else if (extra.action === "paginate" && pag) {
      query.setParams({ page: pag.current, pageSize: pag.pageSize });
    }
  };

  return (
    <>
      <Space
        wrap
        align="center"
        style={{ width: "100%", justifyContent: "space-between", marginBottom: 16 }}
      >
        <Space wrap align="center">
          <Typography.Title level={3} style={{ margin: 0 }}>
            <MailOutlined /> Mail
          </Typography.Title>
          {ownerRef && (
            <Tag closable onClose={clearOwner} color="blue">
              Owner: {ownerLabel(ownerRef)}
            </Tag>
          )}
        </Space>
        {tab === "mailboxes" && (
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={emailDomains.length === 0}
            onClick={() => setCreateOpen(true)}
          >
            New mailbox
          </Button>
        )}
      </Space>
      {/* Card.tabList = tabs attached inside the card, matching the admin
          Users reference layout (Gitea #524). */}
      <Card
        tabList={[
          { key: "mailboxes", tab: "Mailboxes" },
          { key: "groups", tab: "Groups" },
          { key: "stats", tab: "Statistics" },
        ]}
        activeTabKey={tab}
        onTabChange={setTab}
      >
      {tab === "groups" && <AdminGroupsTab />}
      {tab === "stats" && <MailStatsTab />}
      {tab === "mailboxes" && (
        <SearchableTableStringQ<AdminMailbox>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder={t("adminmailpage.search_email_name_domain_owner")}
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          onChange={handleTableChange}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
            showSizeChanger: true,
          }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminmailpage.no_mailboxes")} /> }}
        >
          <Table.Column<AdminMailbox>
            title="Email"
            dataIndex="email"
            key="email"
            sorter
            sortOrder={sortOrderFor("email")}
            render={(v: string) => (
              <Typography.Text style={{ fontFamily: "monospace" }}>{v}</Typography.Text>
            )}
          />
          <Table.Column<AdminMailbox>
            title="Name"
            dataIndex="display_name"
            key="name"
            ellipsis
            sorter
            sortOrder={sortOrderFor("name")}
            render={(v: string) =>
              v ? v : <Typography.Text type="secondary">—</Typography.Text>
            }
          />
          <Table.Column<AdminMailbox>
            title="Domain"
            dataIndex="domain_name"
            key="domain"
            sorter
            sortOrder={sortOrderFor("domain")}
          />
          <Table.Column<AdminMailbox>
            title="Owner"
            dataIndex="user_username"
            key="owner"
            sorter
            sortOrder={sortOrderFor("owner")}
            render={(v: string, row: AdminMailbox) =>
              v ? (
                <Link to={adminLinks.user(row.owner_user_id)}>{v}</Link>
              ) : (
                <Typography.Text type="secondary">—</Typography.Text>
              )
            }
          />
          <Table.Column<AdminMailbox>
            title="Usage / Quota"
            key="usage"
            width={200}
            // GH #1358: sort by space USED (what the column shows), not the
            // quota limit — server sort maps "usage" → m.last_usage_bytes.
            sorter
            sortOrder={sortOrderFor("usage")}
            render={(_: unknown, row: AdminMailbox) => renderMailboxQuota(row)}
          />
          <Table.Column<AdminMailbox>
            title="Status"
            dataIndex="is_disabled"
            key="status"
            width={100}
            sorter
            sortOrder={sortOrderFor("status")}
            render={(disabled: boolean) => renderMailboxStatus(disabled)}
          />
          <Table.Column<AdminMailbox>
            title="Actions"
            key="actions"
            render={(_: unknown, row: AdminMailbox) => (
              <RowActions
                actions={[
                  { key: "webmail", label: "Open webmail", icon: <MailOutlined />, loading: webmail.isLaunching(row.id), onClick: () => webmail.launch(row.id) },
                  { key: "edit", label: "Edit mailbox", icon: <EditOutlined />, onClick: () => setEditTarget(row) },
                  { key: "reset", label: "Reset password", icon: <KeyOutlined />, onClick: () => { resetForm.resetFields(); setResetTarget(row); } },
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: async () => {
                      try {
                        await deleteMutation.mutateAsync({ id: row.id, domainId: row.domain_id });
                        message.success("Mailbox deleted");
                      } catch {
                        message.error("Failed to delete");
                      }
                    },
                    confirm: { title: `Delete ${row.email}?`, description: "All mail in this mailbox will be removed. This cannot be undone.", okText: "Delete" },
                  },
                ]}
              />
            )}
          />
        </SearchableTableStringQ>
      )}
      </Card>

      <CreateMailboxWizardModal
        open={createOpen}
        domains={emailDomains}
        onCancel={() => setCreateOpen(false)}
        onCreated={(resp) => {
          setCreateOpen(false);
          if (resp.password) {
            revealPassword({
              email: resp.email,
              password: resp.password,
              title: t("adminmailpage.mailbox_password"),
            });
          } else {
            message.success("Mailbox created");
          }
        }}
      />

      <EditMailboxModal
        open={editTarget !== null}
        mailbox={editTarget}
        onClose={() => setEditTarget(null)}
      />

      <Modal
        open={resetTarget !== null}
        title={resetTarget ? `Reset password — ${resetTarget.email}` : "Reset password"}
        okText={t("adminmailpage.set_password")}
        confirmLoading={rotateMutation.isPending}
        onOk={submitReset}
        onCancel={() => setResetTarget(null)}
        destroyOnClose
      >
        <Form form={resetForm} layout="vertical" requiredMark={false}>
          <Form.Item
            label={t("adminmailpage.new_password")}
            name="password"
            tooltip={t("adminmailpage.leave_blank_to_auto_generate_auto_generated")}
          >
            <PasswordInput autoComplete="new-password" placeholder="(leave blank to auto-generate)" />
          </Form.Item>
        </Form>
      </Modal>

      <MailboxPasswordRevealModal reveal={reveal} onClose={clearReveal} />
    </>
  );
}

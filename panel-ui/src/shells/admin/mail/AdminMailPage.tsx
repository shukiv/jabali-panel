// AdminMailPage — server-wide mailbox management (admin). Lists every mailbox
// across all domains with add / edit / reset-password / delete. Reuses the
// per-domain create wizard + the shared EditMailboxModal (GH #197 companion).
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { useTabParam } from "../../../hooks/useTabParam";
import {
  App,
  Button,
  Empty,
  Form,
  Input,
  Modal,
  Card,
  Skeleton,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { DeleteOutlined, EditOutlined, KeyOutlined, MailOutlined, PlusOutlined } from "@icons";

import {
  useAdminMailboxes,
  useDeleteMailbox,
  useRotateMailboxPassword,
  type AdminMailbox,
} from "../../../hooks/useMailboxes";
import { useListQuery, useOneQuery } from "../../../hooks/useQueries";
import { useSearchParams, Link } from "react-router";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { ownerResourceCrumbs, ownerLabel, adminLinks } from "../../../components/admin/entityLinks";
import type { Domain } from "../../user/domains/UserDomainList";
import { EditMailboxModal } from "../../../components/mail/EditMailboxModal";
import {
  renderMailboxQuota,
  renderMailboxStatus,
  useMailboxWebmail,
} from "../../../components/mail/mailboxInventory";
import { AdminGroupsTab } from "./AdminGroupsTab";
import { MailStatsTab } from "./MailStatsTab";
import { CreateMailboxWizardModal } from "../../user/mail/CreateMailboxWizardModal";
import { DatabaseUserPasswordModal } from "../../../components/DatabaseUserPasswordModal";
import { PasswordInput } from "../../../components/PasswordInput";
import { RowActions } from "../../../components/RowActions";

export function AdminMailPage() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const { data: rows, isLoading } = useAdminMailboxes();
  const [searchParams, setSearchParams] = useSearchParams();
  const ownerId = searchParams.get("user_id") ?? undefined;
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
  const rotate = useRotateMailboxPassword();
  const webmail = useMailboxWebmail();

  const [tab, setTab] = useTabParam<string>("mailboxes");
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminMailbox | null>(null);
  const [resetTarget, setResetTarget] = useState<AdminMailbox | null>(null);
  const [resetForm] = Form.useForm<{ password?: string }>();
  const [revealed, setRevealed] = useState<{ email: string; password: string } | null>(null);
  const [search, setSearch] = useState("");

  const filtered = useMemo(() => {
    let list = rows ?? [];
    if (ownerId) list = list.filter((m) => m.owner_user_id === ownerId);
    if (!search.trim()) return list;
    const q = search.toLowerCase();
    return list.filter(
      (m) =>
        m.email.toLowerCase().includes(q) ||
        (m.display_name ?? "").toLowerCase().includes(q) ||
        m.domain_name.toLowerCase().includes(q) ||
        (m.user_username ?? "").toLowerCase().includes(q),
    );
  }, [rows, search, ownerId]);

  const submitReset = async () => {
    if (!resetTarget) return;
    const v = await resetForm.validateFields();
    const custom = (v.password ?? "").trim();
    try {
      const resp = await rotate.mutateAsync({
        id: resetTarget.id,
        new_password: custom || undefined,
      });
      const target = resetTarget;
      setResetTarget(null);
      if (resp.password) {
        setRevealed({ email: target.email, password: resp.password });
      } else {
        message.success("Password updated");
      }
    } catch {
      message.error("Failed to reset password");
    }
  };

  useSetBreadcrumbs(
    ownerId
      ? ownerResourceCrumbs({ id: ownerId, username: ownerQ.data?.username }, { key: "mailboxes", label: "Mailboxes" })
      : null,
  );

  if (isLoading && !rows) return <Skeleton active paragraph={{ rows: 6 }} />;

  const ownerRef = ownerId ? { id: ownerId, username: ownerQ.data?.username } : undefined;

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
      <>
        <Space style={{ width: "100%", justifyContent: "flex-start", marginBottom: 16 }} wrap>
          <Input.Search
            placeholder={t("adminmailpage.search_email_name_domain_owner")}
            allowClear
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            style={{ maxWidth: 320 }}
          />
        </Space>

        <Table<AdminMailbox>
          scroll={{ x: "max-content" }}
          rowKey="id"
          dataSource={filtered}
          loading={isLoading}
          pagination={{ defaultPageSize: 25, showSizeChanger: true }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminmailpage.no_mailboxes")} /> }}
          columns={[
            {
              title: "Email",
              dataIndex: "email",
              defaultSortOrder: "ascend",
              sorter: (a, b) => a.email.localeCompare(b.email),
              render: (v: string) => (
                <Typography.Text style={{ fontFamily: "monospace" }}>{v}</Typography.Text>
              ),
            },
            {
              title: "Name",
              dataIndex: "display_name",
              ellipsis: true,
              sorter: (a, b) => (a.display_name ?? "").localeCompare(b.display_name ?? ""),
              render: (v: string) =>
                v ? v : <Typography.Text type="secondary">—</Typography.Text>,
            },
            {
              title: "Domain",
              dataIndex: "domain_name",
              sorter: (a, b) => a.domain_name.localeCompare(b.domain_name),
            },
            {
              title: "Owner",
              dataIndex: "user_username",
              sorter: (a, b) => (a.user_username ?? "").localeCompare(b.user_username ?? ""),
              render: (v: string, row) =>
                v ? (
                  <Link to={adminLinks.user(row.owner_user_id)}>{v}</Link>
                ) : (
                  <Typography.Text type="secondary">—</Typography.Text>
                ),
            },
            {
              title: "Usage / Quota",
              dataIndex: "quota_bytes",
              width: 200,
              // GH #1358: sort by space USED (what the column shows), not the
              // quota limit — the old sorter made "sort by usage" a no-op.
              sorter: (a, b) => (a.last_usage_bytes ?? 0) - (b.last_usage_bytes ?? 0),
              render: (_quota: number, row) => renderMailboxQuota(row),
            },
            {
              title: "Status",
              dataIndex: "is_disabled",
              width: 100,
              sorter: (a, b) => Number(a.is_disabled) - Number(b.is_disabled),
              render: (disabled: boolean) => renderMailboxStatus(disabled),
            },
            {
              title: "Actions",
              key: "actions",
              render: (_, row) => (
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
              ),
            },
          ]}
        />
      </>
      )}
      </Card>

      <CreateMailboxWizardModal
        open={createOpen}
        domains={emailDomains}
        onCancel={() => setCreateOpen(false)}
        onCreated={(resp) => {
          setCreateOpen(false);
          if (resp.password) {
            setRevealed({ email: resp.email, password: resp.password });
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
        confirmLoading={rotate.isPending}
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

      {revealed && (
        <DatabaseUserPasswordModal
          open
          username={revealed.email}
          password={revealed.password}
          title={t("adminmailpage.mailbox_password")}
          onClose={() => setRevealed(null)}
        />
      )}
    </>
  );
}

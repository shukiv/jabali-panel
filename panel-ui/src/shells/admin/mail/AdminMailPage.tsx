// AdminMailPage — server-wide mailbox management (admin). Lists every mailbox
// across all domains with add / edit / reset-password / delete. Reuses the
// per-domain create wizard + the shared EditMailboxModal (GH #197 companion).
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
  Progress,
  Skeleton,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { DeleteOutlined, EditOutlined, KeyOutlined, MailOutlined, PlusOutlined } from "@icons";

import {
  useAdminMailboxes,
  useDeleteMailbox,
  useMintMailboxSSO,
  useRotateMailboxPassword,
  type AdminMailbox,
} from "../../../hooks/useMailboxes";
import { useListQuery, useOneQuery } from "../../../hooks/useQueries";
import { useSearchParams, Link } from "react-router";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { ownerResourceCrumbs, ownerLabel, adminLinks } from "../../../components/admin/entityLinks";
import type { Domain } from "../../user/domains/UserDomainList";
import { EditMailboxModal } from "../../../components/mail/EditMailboxModal";
import { AdminGroupsTab } from "./AdminGroupsTab";
import { CreateMailboxWizardModal } from "../../user/mail/CreateMailboxWizardModal";
import { DatabaseUserPasswordModal } from "../../../components/DatabaseUserPasswordModal";
import { PasswordInput } from "../../../components/PasswordInput";
import { RowActions } from "../../../components/RowActions";

function formatBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function AdminMailPage() {
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
  const ssoMutation = useMintMailboxSSO();

  const openWebmail = (row: AdminMailbox) => {
    // Sever the opener link so the webmail tab can't reverse-tabnab the panel.
    // (noopener can't be passed to window.open here — it returns null and
    // breaks the synchronous-popup-then-set-href pattern that dodges blockers.)
    const popup = window.open("about:blank", "_blank");
    if (popup) {
      try {
        popup.opener = null;
      } catch {
        // older Safari — best effort
      }
    }
    ssoMutation.mutate(
      { id: row.id },
      {
        onSuccess: (data) => {
          if (popup && data?.url) popup.location.href = data.url;
        },
        onError: (err) => {
          const code = (err as { response?: { data?: { error?: string } } })?.response
            ?.data?.error;
          popup?.close();
          if (code === "sso_unavailable_rotate_password") {
            message.error(
              "Rotate the mailbox password first — SSO material is populated on rotation.",
            );
          } else {
            message.error("Failed to open webmail");
          }
        },
      },
    );
  };

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
        ]}
        activeTabKey={tab}
        onTabChange={setTab}
      >
      {tab === "groups" && <AdminGroupsTab />}
      {tab === "mailboxes" && (
      <>
        <Space style={{ width: "100%", justifyContent: "flex-start", marginBottom: 16 }} wrap>
          <Input.Search
            placeholder="Search email, name, domain, owner"
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
          pagination={{ pageSize: 25, showSizeChanger: true }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No mailboxes" /> }}
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
              title: "Quota",
              dataIndex: "quota_bytes",
              width: 200,
              sorter: (a, b) => (a.quota_bytes ?? 0) - (b.quota_bytes ?? 0),
              render: (quota: number, row) => {
                const used = row.last_usage_bytes ?? 0;
                const pct = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
                return (
                  <Tooltip title={`${formatBytes(used)} of ${formatBytes(quota)}`}>
                    <Progress
                      percent={pct}
                      size="small"
                      status={pct >= 90 ? "exception" : "normal"}
                      format={() => `${formatBytes(used)} / ${formatBytes(quota)}`}
                    />
                  </Tooltip>
                );
              },
            },
            {
              title: "Status",
              dataIndex: "is_disabled",
              width: 100,
              sorter: (a, b) => Number(a.is_disabled) - Number(b.is_disabled),
              render: (disabled: boolean) =>
                disabled ? <Tag color="red">disabled</Tag> : <Tag color="green">active</Tag>,
            },
            {
              title: "Actions",
              key: "actions",
              render: (_, row) => (
                <RowActions
                  actions={[
                    { key: "webmail", label: "Open webmail", icon: <MailOutlined />, loading: ssoMutation.isPending && ssoMutation.variables?.id === row.id, onClick: () => openWebmail(row) },
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
        okText="Set password"
        confirmLoading={rotate.isPending}
        onOk={submitReset}
        onCancel={() => setResetTarget(null)}
        destroyOnClose
      >
        <Form form={resetForm} layout="vertical" requiredMark={false}>
          <Form.Item
            label="New password"
            name="password"
            tooltip="Leave blank to auto-generate. Auto-generated passwords are shown exactly once."
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
          title="Mailbox password"
          onClose={() => setRevealed(null)}
        />
      )}
    </>
  );
}

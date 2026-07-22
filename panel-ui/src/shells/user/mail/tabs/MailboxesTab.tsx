// MailboxesTab — cross-domain mailbox list + actions.
//
// Lists mailboxes from all user domains in a single table. Extracted from
// the original UserMailboxesPage. Merges results from per-domain list queries
// client-side and provides password rotation, SSO mint, and delete actions.
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import {
  Button,
  Empty,
  Form,
  Modal,
  Progress,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import { RowActions } from "../../../../components/RowActions";
import { SearchableTableStringQ } from "../../../../components/SearchableTable";
import {
  DeleteOutlined,
  EditOutlined,
  ClockCircleOutlined,
  KeyOutlined,
  MailOutlined,
} from "@icons";
import { useQueries } from "@tanstack/react-query";

import { AutoReplyModal } from "../AutoReplyModal";
import { type Autoresponder } from "../../../../hooks/useAutoresponders";
import { useForwarders } from "../../../../hooks/useForwarders";

import { apiClient } from "../../../../apiClient";
import {
  useDeleteMailbox,
  useMintMailboxSSO,
  useRotateMailboxPassword,
  type Mailbox,
} from "../../../../hooks/useMailboxes";
import { useListQuery } from "../../../../hooks/useQueries";
import type { Domain } from "../../domains/UserDomainList";
import { DatabaseUserPasswordModal } from "../../../../components/DatabaseUserPasswordModal";
import { PasswordInput } from "../../../../components/PasswordInput";
import { EditMailboxModal } from "../../../../components/mail/EditMailboxModal";

type MailboxRow = Mailbox & { domain_name: string };
type GroupMembership = {
  group_id: string;
  group_name: string;
  group_email: string;
};

function formatBytes(n: number): string {
  if (n === 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  const v = n / Math.pow(1024, i);
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export const MailboxesTab = () => {
  const { t } = useTranslation();
  const { items: domains, isLoading: loadingDomains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });

  const emailEnabledDomains = useMemo(
    () => domains.filter((d) => d.email_enabled),
    [domains],
  );

  const mailboxResults = useQueries({
    queries: emailEnabledDomains.map((d) => ({
      queryKey: ["list", "mailboxes", d.id, { page: 1, pageSize: 200 }],
      queryFn: async () => {
        const { data } = await apiClient.get<{ data: Mailbox[]; total: number }>(
          `/domains/${d.id}/mailboxes?page=1&page_size=200&sort=local_part&order=asc`,
        );
        return { items: data.data ?? [], domain: d };
      },
    })),
  });

  const membershipResults = useQueries({
    queries: emailEnabledDomains.map((d) => ({
      queryKey: ["list", "mailbox-group-memberships", d.id],
      queryFn: async () => {
        const { data } = await apiClient.get<{
          data: Record<string, GroupMembership[]>;
        }>(`/domains/${d.id}/mailbox-group-memberships`);
        return data.data ?? {};
      },
    })),
  });

  const groupsByMailbox = useMemo(() => {
    const out: Record<string, GroupMembership[]> = {};
    for (const r of membershipResults) {
      if (!r.data) continue;
      for (const [mbID, groups] of Object.entries(r.data)) {
        out[mbID] = groups;
      }
    }
    return out;
  }, [membershipResults]);


  const rows: MailboxRow[] = useMemo(() => {
    const out: MailboxRow[] = [];
    for (const r of mailboxResults) {
      if (!r.data) continue;
      for (const mb of r.data.items) {
        out.push({ ...mb, domain_name: r.data.domain.name });
      }
    }
    return out;
  }, [mailboxResults]);

  // Per-mailbox automatic-replies (autoresponder) fanout — one GET each,
  // shares the cache with useAutoresponder via the matching query key.
  // GH #240: surfaced as a column + kebab action on this tab (the
  // standalone Autoresponders tab was removed).
  const arResults = useQueries({
    queries: emailEnabledDomains.map((d) => ({
      queryKey: ["autoresponders", "by-domain", d.id],
      queryFn: async () => {
        const { data } = await apiClient.get<{
          data: Record<string, Autoresponder>;
        }>(`/domains/${d.id}/autoresponders`);
        return data.data ?? {};
      },
    })),
  });

  const arByMailbox = useMemo(() => {
    const out: Record<string, Autoresponder> = {};
    for (const r of arResults) {
      if (!r.data) continue;
      for (const [mbID, ar] of Object.entries(r.data)) {
        out[mbID] = ar;
      }
    }
    return out;
  }, [arResults]);

  // Aliases + external forwards per mailbox (GH #237). One bulk query
  // across all the caller's mailboxes; grouped client-side so each row can
  // show its aliases under the address and a forwarding indicator.
  const { data: forwarders = [] } = useForwarders();
  const fwdByMailbox = useMemo(() => {
    const out: Record<
      string,
      { aliases: string[]; forwards: { target: string; keep_copy: boolean }[] }
    > = {};
    for (const f of forwarders) {
      if (!f.enabled) continue;
      const bucket = (out[f.mailbox_id] ??= { aliases: [], forwards: [] });
      if (f.type === "alias" && f.local_part) bucket.aliases.push(f.local_part);
      else if (f.type === "external")
        bucket.forwards.push({ target: f.target, keep_copy: f.keep_copy });
    }
    return out;
  }, [forwarders]);


  const [passwordModal, setPasswordModal] = useState<{
    email: string;
    password: string;
    title: string;
  } | null>(null);
  const [rotatingId, setRotatingId] = useState<string | null>(null);
  const [resetTarget, setResetTarget] = useState<MailboxRow | null>(null);
  const [editTarget, setEditTarget] = useState<MailboxRow | null>(null);
  const [arTarget, setArTarget] = useState<MailboxRow | null>(null);
  const [resetForm] = Form.useForm<{ password?: string }>();

  const deleteMutation = useDeleteMailbox();
  const rotateMutation = useRotateMailboxPassword();
  const ssoMutation = useMintMailboxSSO();

  const openReset = (row: MailboxRow) => {
    resetForm.resetFields();
    setResetTarget(row);
  };

  const submitReset = async () => {
    if (!resetTarget) return;
    const values = await resetForm.validateFields();
    const custom = (values.password ?? "").trim();
    setRotatingId(resetTarget.id);
    try {
      const resp = await rotateMutation.mutateAsync({
        id: resetTarget.id,
        new_password: custom || undefined,
      });
      setResetTarget(null);
      if (resp.password) {
        // No custom password supplied -> server generated one; reveal once.
        setPasswordModal({
          email: resetTarget.email,
          password: resp.password,
          title: "New mailbox password (auto-generated)",
        });
      } else {
        message.success("Password updated");
      }
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        "Failed to reset password";
      message.error(msg);
    } finally {
      setRotatingId(null);
    }
  };

  const openWebmail = (row: MailboxRow) => {
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

  const loading = loadingDomains || mailboxResults.some((r) => r.isLoading);
  const [search, setSearch] = useState("");
  const filteredRows = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter(
      (r) =>
        r.email.toLowerCase().includes(q) ||
        (r.domain_name ?? "").toLowerCase().includes(q),
    );
  }, [rows, search]);

  if (loading && rows.length === 0) {
    return <Skeleton active paragraph={{ rows: 4 }} />;
  }

  if (rows.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("mailboxestab.no_mailboxes_yet")} />;
  }

  return (
    <>
      <SearchableTableStringQ<MailboxRow>
        scroll={{ x: "max-content" }}
        rowKey="id"
        loading={loading && rows.length === 0}
        dataSource={filteredRows}
        searchPlaceholder="Search email, name, domain…"
        initialSearch={search}
        onSearchChange={setSearch}
        pagination={{ pageSize: 20 }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("mailboxestab.no_mailboxes")} /> }}
        columns={[
          {
            title: "Mailbox",
            dataIndex: "email",
            ellipsis: true,
            sorter: (a, b) => a.email.localeCompare(b.email),
            render: (v: string, record: MailboxRow) => {
              const fwd = fwdByMailbox[record.id];
              return (
                <span style={{ display: "inline-flex", flexDirection: "column" }}>
                    {record.display_name ? (
                      <Typography.Text strong>
                        {record.display_name}
                      </Typography.Text>
                    ) : null}
                    <Typography.Text
                      type={record.display_name ? "secondary" : undefined}
                      style={{ fontFamily: "monospace", fontSize: record.display_name ? 12 : undefined }}
                    >
                      {v}
                    </Typography.Text>
                    {fwd?.aliases.length ? (
                      <Typography.Text
                        type="secondary"
                        style={{ fontSize: 12, fontFamily: "monospace" }}
                      >
                        {fwd.aliases.map((a) => `+${a}`).join("  ")}
                      </Typography.Text>
                    ) : null}
                    {fwd?.forwards.length ? (
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        →{" "}
                        {fwd.forwards.map((f) => f.target).join(", ")}
                        {fwd.forwards.some((f) => f.keep_copy) ? " (keeps copy)" : ""}
                      </Typography.Text>
                    ) : null}
                </span>
              );
            },
          },
          {
            title: "Domain",
            dataIndex: "domain_name",
            sorter: (a, b) => a.domain_name.localeCompare(b.domain_name),
            width: 220,
          },
          {
            title: "Groups",
            key: "groups",
            width: 200,
            render: (_: unknown, record: MailboxRow) => {
              const groups = groupsByMailbox[record.id] ?? [];
              if (groups.length === 0)
                return <Typography.Text type="secondary">—</Typography.Text>;
              return (
                <Space size={[0, 4]} wrap>
                  {groups.map((g) => (
                    <Tag key={g.group_id}>{g.group_name}</Tag>
                  ))}
                </Space>
              );
            },
          },
          {
            title: "Auto replies",
            key: "auto_replies",
            width: 110,
            align: "center" as const,
            render: (_: unknown, record: MailboxRow) => {
              const ar = arByMailbox[record.id];
              if (!ar?.enabled)
                return <Typography.Text type="secondary">—</Typography.Text>;
              const fmt = (d: string | null) =>
                d ? new Date(d).toLocaleDateString() : "—";
              const dates =
                ar.from_date || ar.to_date
                  ? `${fmt(ar.from_date)} → ${fmt(ar.to_date)}`
                  : "always on";
              return (
                <Tooltip
                  title={
                    <span>
                      <strong>{ar.subject || "(default subject)"}</strong>
                      <br />
                      {dates}
                      {ar.text_body ? (
                        <>
                          <br />
                          {ar.text_body.slice(0, 140)}
                          {ar.text_body.length > 140 ? "…" : ""}
                        </>
                      ) : null}
                    </span>
                  }
                >
                  <Button
                    type="text"
                    icon={<ClockCircleOutlined style={{ color: "#52c41a" }} />}
                    aria-label={t("mailboxestab.automatic_replies_active")}
                    onClick={() => setArTarget(record)}
                  />
                </Tooltip>
              );
            },
          },
          {
            title: "Quota",
            dataIndex: "quota_bytes",
            sorter: (a, b) => (a.quota_bytes ?? 0) - (b.quota_bytes ?? 0),
            width: 220,
            render: (quota: number, row) => {
              const used = row.last_usage_bytes ?? 0;
              const pct =
                quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
              return (
                <Tooltip title={`${formatBytes(used)} of ${formatBytes(quota)}`}>
                  <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
                    <Typography.Text style={{ fontSize: 12 }}>
                      {`${formatBytes(used)} / ${formatBytes(quota)}`}
                    </Typography.Text>
                    <Progress
                      percent={pct}
                      size="small"
                      showInfo={false}
                      status={pct >= 90 ? "exception" : "normal"}
                    />
                  </div>
                </Tooltip>
              );
            },
          },
          {
            title: "Last usage",
            dataIndex: "last_usage_at",
            sorter: (a, b) => (a.last_usage_at ? +new Date(a.last_usage_at) : 0) - (b.last_usage_at ? +new Date(b.last_usage_at) : 0),
            width: 160,
            render: (v: string | null | undefined) =>
              v ? (
                new Date(v).toLocaleString()
              ) : (
                <Typography.Text type="secondary">never</Typography.Text>
              ),
          },
          {
            title: "Status",
            dataIndex: "is_disabled",
            sorter: (a, b) => Number(a.is_disabled) - Number(b.is_disabled),
            width: 100,
            render: (disabled: boolean) =>
              disabled ? (
                <Tag color="red">disabled</Tag>
              ) : (
                <Tag color="green">active</Tag>
              ),
          },
          {
            title: "Actions",
            width: 120,
            render: (_, row) => (
              <RowActions
                actions={[
                  {
                    key: "webmail",
                    label: "Webmail",
                    icon: <MailOutlined />,
                    tooltip: "Open webmail for this mailbox",
                    loading: ssoMutation.isPending && ssoMutation.variables?.id === row.id,
                    onClick: () => openWebmail(row),
                  },
                  { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => setEditTarget(row) },
                  { key: "resetpw", label: "Reset password", icon: <KeyOutlined />, onClick: () => openReset(row) },
                  { key: "autoreply", label: "Automatic replies", icon: <ClockCircleOutlined />, onClick: () => setArTarget(row) },
                  {
                    key: "remove",
                    label: "Remove",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: () => {
                      Modal.confirm({
                        title: `Delete ${row.email}?`,
                        content: "All mail in this mailbox will be removed. This cannot be undone.",
                        okText: "Delete",
                        okType: "danger",
                        onOk: async () => {
                          try {
                            await deleteMutation.mutateAsync({ id: row.id, domainId: row.domain_id });
                            message.success("Mailbox deleted");
                          } catch (err) {
                            const msg =
                              (err as { response?: { data?: { detail?: string } } })?.response?.data
                                ?.detail ?? "Failed to delete";
                            message.error(msg);
                          }
                        },
                      });
                    },
                  },
                ]}
              />
            ),
          },
        ]}
      />

      <EditMailboxModal
        open={editTarget !== null}
        mailbox={editTarget}
        onClose={() => setEditTarget(null)}
      />

      <AutoReplyModal
        open={arTarget !== null}
        mailboxId={arTarget?.id ?? ""}
        email={arTarget?.email ?? ""}
        current={arTarget ? arByMailbox[arTarget.id] ?? null : null}
        onClose={() => setArTarget(null)}
      />

      <Modal
        open={resetTarget !== null}
        title={resetTarget ? `Reset password — ${resetTarget.email}` : "Reset password"}
        okText={t("mailboxestab.set_password")}
        confirmLoading={resetTarget ? rotatingId === resetTarget.id : false}
        onOk={submitReset}
        onCancel={() => setResetTarget(null)}
        destroyOnClose
      >
        <Form form={resetForm} layout="vertical" requiredMark={false}>
          <Form.Item
            label={t("mailboxestab.new_password")}
            name="password"
            tooltip={t("mailboxestab.leave_blank_to_auto_generate_auto_generated")}
          >
            <PasswordInput
              autoComplete="new-password"
              placeholder="(leave blank to auto-generate)"
            />
          </Form.Item>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Use the dice button to generate a strong password, or type your own.
          </Typography.Text>
        </Form>
      </Modal>

      {passwordModal && (
        <DatabaseUserPasswordModal
          open={true}
          username={passwordModal.email}
          password={passwordModal.password}
          title={passwordModal.title}
          onClose={() => setPasswordModal(null)}
        />
      )}
    </>
  );
};

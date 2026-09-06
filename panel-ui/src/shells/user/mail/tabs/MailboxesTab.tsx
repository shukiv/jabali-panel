// MailboxesTab — cross-domain mailbox list + actions.
//
// Lists mailboxes from all user domains in a single table. Extracted from
// the original UserMailboxesPage. Merges results from per-domain list queries
// client-side and provides password rotation, SSO mint, and delete actions.
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { Button, Empty, Skeleton, Space, Tag, Tooltip, Typography } from "antd";
import type { TableProps } from "antd";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts
import { RowActions } from "../../../../components/RowActions";
import { SearchableTableStringQ } from "../../../../components/SearchableTable";
import {
  DeleteOutlined,
  EditOutlined,
  ClockCircleOutlined,
  KeyOutlined,
  MailOutlined,
  CalendarCheckOutlined,
} from "@icons";
import { useQueries } from "@tanstack/react-query";

import { AutoReplyModal } from "../AutoReplyModal";
import { type Autoresponder } from "../../../../hooks/useAutoresponders";
import { useForwarders } from "../../../../hooks/useForwarders";

import { apiClient } from "../../../../apiClient";
import {
  useDeleteMailbox,
  type Mailbox,
} from "../../../../hooks/useMailboxes";
import {
  MailboxPasswordRevealModal,
  renderMailboxQuota,
  renderMailboxStatus,
  useMailboxPasswordReset,
  useMailboxWebmail,
} from "../../../../components/mail/mailboxInventory";
import { useListQuery } from "../../../../hooks/useQueries";
import { useTableURL } from "../../../../hooks/useTableURL";
import type { Domain } from "../../../../components/domains/types";
import { EditMailboxModal } from "../../../../components/mail/EditMailboxModal";
import { MailSyncInfoModal } from "../../../../components/mail/MailSyncInfoModal";

type MailboxRow = Mailbox & { domain_name: string };
type GroupMembership = {
  group_id: string;
  group_name: string;
  group_email: string;
};

// GH #1387: when domainId is set (the per-domain Mail Domains drill-down), the
// tab scopes to that one domain and hides the Domain column; unset = the flat
// cross-domain view (unchanged).
export const MailboxesTab = ({ domainId }: { domainId?: string } = {}) => {
  const { t } = useTranslation();
  const { items: domains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });

  const emailEnabledDomains = useMemo(
    () => domains.filter((d) => d.email_enabled && (!domainId || d.id === domainId)),
    [domains, domainId],
  );

  // JAB-370 Workspace: the mailbox rows come from ONE owner-scoped,
  // server-paginated query — GET /me/mailboxes spanning every domain the caller
  // owns (cross-domain view), or the per-domain endpoint when this tab is
  // embedded in the Mail Domains drill-down (domainId set). This replaces the
  // one-request-per-domain fan-out that capped each domain at 200 rows and never
  // paginated across domains. Search/sort/pagination are all server-authoritative.
  const resource = domainId ? `domains/${domainId}/mailboxes` : "me/mailboxes";
  const query = useTableURL<Mailbox & { domain_name?: string }>({
    resource,
    defaultSort: "email",
    defaultOrder: "asc",
    defaultPageSize: 20,
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


  // The per-domain endpoint returns rows without a domain_name (that column is
  // hidden in the drill-down anyway); backfill it from the domains list so the
  // Calendar/contacts modal still has a domain to build DAV URLs from.
  const domainNameById = useMemo(() => {
    const m: Record<string, string> = {};
    for (const d of domains) m[d.id] = d.name;
    return m;
  }, [domains]);
  const rows: MailboxRow[] = useMemo(
    () =>
      query.items.map((r) => ({
        ...r,
        domain_name: r.domain_name ?? domainNameById[r.domain_id] ?? "",
      })),
    [query.items, domainNameById],
  );

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


  const [editTarget, setEditTarget] = useState<MailboxRow | null>(null);
  const [arTarget, setArTarget] = useState<MailboxRow | null>(null);
  const [syncTarget, setSyncTarget] = useState<MailboxRow | null>(null);

  const deleteMutation = useDeleteMailbox();
  const { rotate: rotatePassword, rotatingId, reveal, clearReveal } = useMailboxPasswordReset();
  const webmail = useMailboxWebmail();

  const loading = query.isLoading;

  // Map AntD's per-column sort event onto the server sort/order params. Sortable
  // columns set `sorter: true` (no local comparator) so sorting is authoritative
  // across ALL pages, not just the visible one; the repo whitelists the key.
  const sortOrderFor = (key: string): "ascend" | "descend" | null =>
    query.params.sort === key ? (query.params.order === "asc" ? "ascend" : "descend") : null;
  const handleTableChange: TableProps<MailboxRow>["onChange"] = (pag, _filters, sorter, extra) => {
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

  if (loading && rows.length === 0) {
    return <Skeleton active paragraph={{ rows: 4 }} />;
  }

  return (
    <>
      <SearchableTableStringQ<MailboxRow>
        scroll={{ x: "max-content" }}
        rowKey="id"
        loading={loading}
        dataSource={rows}
        searchPlaceholder="Search email, name, domain…"
        initialSearch={query.params.q}
        onSearchChange={(q) => query.setParams({ q, page: 1 })}
        onChange={handleTableChange}
        pagination={{
          current: query.params.page,
          pageSize: query.params.pageSize,
          total: query.total,
          showSizeChanger: true,
        }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("mailboxestab.no_mailboxes")} /> }}
        columns={[
          {
            title: "Mailbox",
            dataIndex: "email",
            key: "email",
            ellipsis: true,
            sorter: true,
            sortOrder: sortOrderFor("email"),
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
          ...(domainId
            ? []
            : [
                {
                  title: "Domain",
                  dataIndex: "domain_name",
                  key: "domain",
                  sorter: true,
                  sortOrder: sortOrderFor("domain"),
                  width: 220,
                },
              ]),
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
            title: "Usage / Quota",
            dataIndex: "quota_bytes",
            key: "usage",
            // GH #1358: the column shows space USED (used / quota + fill bar), so
            // sort by used bytes — not the quota limit. The server "usage" sort
            // key maps to m.last_usage_bytes (mailboxDirectorySortKeys).
            sorter: true,
            sortOrder: sortOrderFor("usage"),
            width: 220,
            render: (_quota: number, row) => renderMailboxQuota(row),
          },
          {
            title: "Last usage",
            dataIndex: "last_usage_at",
            // Not a server-side sort key (the directory projection sorts by
            // usage bytes, not last-usage timestamp), so this column is not
            // sortable under server pagination — a client comparator would only
            // reorder the visible page and mislead.
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
            key: "status",
            sorter: true,
            sortOrder: sortOrderFor("status"),
            width: 100,
            render: (disabled: boolean) => renderMailboxStatus(disabled),
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
                    loading: webmail.isLaunching(row.id),
                    onClick: () => webmail.launch(row.id),
                  },
                  { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => setEditTarget(row) },
                  // Send-only mailboxes (GH #371 relays like noreply@) have no
                  // inbox/calendar/contacts, so skip the CalDAV/CardDAV action.
                  ...(!row.send_only
                    ? [
                        {
                          key: "sync",
                          label: "Calendar & contacts",
                          icon: <CalendarCheckOutlined />,
                          tooltip: "CalDAV / CardDAV URLs for Thunderbird, Apple Mail, etc.",
                          onClick: () => setSyncTarget(row),
                        },
                      ]
                    : []),
                  { key: "resetpw", label: "Rotate password", icon: <KeyOutlined />, loading: rotatingId === row.id, onClick: () => rotatePassword({ id: row.id, email: row.email, title: "New mailbox password" }) },
                  { key: "autoreply", label: "Automatic replies", icon: <ClockCircleOutlined />, onClick: () => setArTarget(row) },
                  {
                    key: "remove",
                    label: "Remove",
                    icon: <DeleteOutlined />,
                    danger: true,
                    // Shared RowActions confirm modal (the other two inventories
                    // use the same declarative prop) — no hand-rolled modal here.
                    onClick: async () => {
                      try {
                        await deleteMutation.mutateAsync({ id: row.id, domainId: row.domain_id });
                        feedback.message.success("Mailbox deleted");
                      } catch (err) {
                        const msg =
                          (err as { response?: { data?: { detail?: string } } })?.response?.data
                            ?.detail ?? "Failed to delete";
                        feedback.message.error(msg);
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

      <MailSyncInfoModal
        open={syncTarget !== null}
        email={syncTarget?.email ?? ""}
        domain={syncTarget?.domain_name ?? ""}
        onClose={() => setSyncTarget(null)}
      />

      <MailboxPasswordRevealModal reveal={reveal} onClose={clearReveal} />
    </>
  );
};
